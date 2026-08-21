package service

import (
	"encoding/json"
	"net/netip"
	"sort"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/isp"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ISPBlockOutboundTag is the blackhole every ISP-locked client falls into when
// it connects from a network it is not allowed to use.
const ISPBlockOutboundTag = "gucci-isp-block"

// ispLockEntry pairs a client's email (which is what xray routing matches on
// via the `user` field) with the networks it may connect from.
type ispLockEntry struct {
	email string
	ids   []string
}

// ispLockStatus reports what the last config build was able to enforce. It
// feeds the panel's UI badge so an operator can tell a real lock from one that
// silently did nothing.
type ispLockStatus struct {
	Groups    int      `json:"groups"`
	Clients   int      `json:"clients"`
	Skipped   []string `json:"skipped,omitempty"`
	LastError string   `json:"lastError,omitempty"`
}

var lastISPLockStatus ispLockStatus

// ISPLockStatus returns the outcome of the most recent injection.
func ISPLockStatus() ispLockStatus { return lastISPLockStatus }

// injectISPLocks appends a blackhole outbound and prepends one routing rule per
// distinct ISP selection:
//
//	{"type":"field","user":[...],"source":["ext:gucci-isp.dat:!ISPLOCK…"],
//	 "outboundTag":"gucci-isp-block"}
//
// The "!" inverts the source match, so the rule fires exactly when the client
// connects from outside its allowed networks and its traffic is dropped. Rules
// go in front of the operator's own rules so a custom routing table cannot
// accidentally let a locked client out through another path; every other client
// is untouched.
//
// Anything that cannot be enforced (unknown ISP, no announced prefixes, asset
// not writable) is skipped and reported instead of being silently approximated.
func injectISPLocks(cfg *xray.Config, entries []ispLockEntry) {
	status := ispLockStatus{}
	defer func() { lastISPLockStatus = status }()

	if len(entries) == 0 {
		return
	}

	type group struct {
		ids    []string
		emails []string
	}
	groups := make(map[string]*group)
	for _, e := range entries {
		ids := isp.Sanitize(e.ids)
		if len(ids) == 0 || e.email == "" {
			continue
		}
		code := isp.GroupCode(ids)
		if code == "" {
			continue
		}
		g, ok := groups[code]
		if !ok {
			g = &group{ids: ids}
			groups[code] = g
		}
		g.emails = append(g.emails, e.email)
	}
	if len(groups) == 0 {
		return
	}

	prefixSets := make(map[string][]netip.Prefix, len(groups))
	for code, g := range groups {
		prefixes := isp.Prefixes(g.ids)
		if len(prefixes) == 0 {
			// Enforcing this would black-hole the client on every network.
			status.Skipped = append(status.Skipped, g.emails...)
			delete(groups, code)
			continue
		}
		prefixSets[code] = prefixes
	}
	if len(groups) == 0 {
		return
	}

	if _, err := isp.WriteGeoDat(config.GetBinFolderPath(), prefixSets); err != nil {
		// Fail open: a client keeps working on every network rather than the
		// whole panel losing its config because an asset could not be written.
		logger.Warning("isp lock: cannot write geo asset, restriction not applied:", err)
		status.LastError = err.Error()
		for _, g := range groups {
			status.Skipped = append(status.Skipped, g.emails...)
		}
		return
	}

	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("isp lock: routing section is unparsable, skipping injection:", err)
			status.LastError = err.Error()
			return
		}
	}
	existing, _ := routing["rules"].([]any)

	codes := make([]string, 0, len(groups))
	for code := range groups {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	newRules := make([]any, 0, len(codes))
	for _, code := range codes {
		g := groups[code]
		if _, ok := prefixSets[code]; !ok {
			status.Skipped = append(status.Skipped, g.emails...)
			continue
		}
		emails := append([]string(nil), g.emails...)
		sort.Strings(emails)
		users := make([]any, 0, len(emails))
		for _, email := range emails {
			users = append(users, email)
		}
		newRules = append(newRules, map[string]any{
			"type":        "field",
			"ruleTag":     "gucci-isp-lock-" + code,
			"user":        users,
			"source":      []any{isp.GeoDatRef(code)},
			"outboundTag": ISPBlockOutboundTag,
		})
		status.Groups++
		status.Clients += len(emails)
	}
	if len(newRules) == 0 {
		return
	}

	if !outboundTagExists(cfg.OutboundConfigs, ISPBlockOutboundTag) {
		appended, err := appendBlackholeOutbound(cfg.OutboundConfigs, ISPBlockOutboundTag)
		if err != nil {
			logger.Warning("isp lock: cannot append blackhole outbound, skipping injection:", err)
			status = ispLockStatus{LastError: err.Error()}
			return
		}
		cfg.OutboundConfigs = appended
	}

	routing["rules"] = append(newRules, existing...)
	rebuilt, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("isp lock: failed to rebuild routing section, skipping injection:", err)
		status = ispLockStatus{LastError: err.Error()}
		return
	}
	cfg.RouterConfig = json_util.RawMessage(rebuilt)
	logger.Debugf("isp lock: %d rule(s) covering %d client(s)", status.Groups, status.Clients)
}

// appendBlackholeOutbound adds a drop-everything outbound with the given tag to
// an existing outbounds array without disturbing the operator's own entries.
func appendBlackholeOutbound(outbounds json_util.RawMessage, tag string) (json_util.RawMessage, error) {
	var list []any
	if len(outbounds) > 0 {
		if err := json.Unmarshal(outbounds, &list); err != nil {
			return nil, err
		}
	}
	list = append(list, map[string]any{
		"tag":      tag,
		"protocol": "blackhole",
		"settings": map[string]any{"response": map[string]any{"type": "none"}},
	})
	raw, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	return json_util.RawMessage(raw), nil
}
