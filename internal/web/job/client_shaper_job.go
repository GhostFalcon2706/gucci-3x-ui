package job

import (
	"encoding/json"
	"net/netip"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/shaper"
)

// ClientShaperJob keeps the kernel's traffic-control classes in sync with the
// per-client speed tiers configured in the panel.
//
// Every tick it pairs the IP addresses the IP-limit tracker has observed for
// each client with that client's speed level, and hands the resulting plan to
// the shaper. Clients on level 0 (unlimited) are left unclassified, so the
// default class keeps giving them the full line rate.
//
// On a host that cannot shape (no CAP_NET_ADMIN, no tc, non-Linux) the job logs
// the reason once and then stays idle — it never pretends the limit is active.
type ClientShaperJob struct {
	warnOnce sync.Once
}

func NewClientShaperJob() *ClientShaperJob { return &ClientShaperJob{} }

func (j *ClientShaperJob) Run() {
	db := database.GetDB()
	if db == nil {
		return
	}

	var levels []struct {
		Email      string
		SpeedLevel int
	}
	if err := db.Model(&model.ClientRecord{}).
		Select("email", "speed_level").
		Where("speed_level > 0 AND enable = ?", true).
		Scan(&levels).Error; err != nil {
		logger.Warning("client shaper: reading speed levels failed:", err)
		return
	}

	capability := shaper.Detect()
	if !capability.Available {
		if len(levels) > 0 {
			j.warnOnce.Do(func() {
				logger.Warning("client shaper: speed limits are configured but cannot be enforced on this host —", capability.Reason)
			})
		}
		return
	}

	if len(levels) == 0 {
		// Nothing to shape: drop back to an empty plan so a removed limit is
		// released immediately instead of at the next restart.
		if err := shaper.Apply(capability.Interface, ladder(), shaper.Plan{}); err != nil {
			logger.Warning("client shaper: clearing filters failed:", err)
		}
		return
	}

	levelByEmail := make(map[string]int, len(levels))
	emails := make([]string, 0, len(levels))
	for _, row := range levels {
		lvl := model.NormalizeSpeedLevel(row.SpeedLevel)
		if lvl == 0 {
			continue
		}
		levelByEmail[row.Email] = lvl
		emails = append(emails, row.Email)
	}
	if len(emails) == 0 {
		return
	}

	var ipRows []model.InboundClientIps
	if err := db.Where("client_email IN ?", emails).Find(&ipRows).Error; err != nil {
		logger.Warning("client shaper: reading client IPs failed:", err)
		return
	}

	plan := make(shaper.Plan, len(ipRows))
	for _, row := range ipRows {
		level := levelByEmail[row.ClientEmail]
		if level == 0 {
			continue
		}
		for _, raw := range parseTrackedIps(row.Ips) {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				continue
			}
			addr = addr.Unmap()
			// The slowest tier wins when one address is shared by clients on
			// different tiers — otherwise a fast client on the same NAT would
			// lift the cap of a slow one.
			if current, ok := plan[addr]; !ok || level > current {
				plan[addr] = level
			}
		}
	}

	if err := shaper.Apply(capability.Interface, ladder(), plan); err != nil {
		logger.Warning("client shaper: applying traffic control failed:", err)
	}
}

// parseTrackedIps decodes the inbound_client_ips storage, which holds either a
// JSON array or (on legacy rows) a double-encoded JSON string.
func parseTrackedIps(raw string) []string {
	if raw == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return list
	}
	var nested string
	if err := json.Unmarshal([]byte(raw), &nested); err == nil {
		if err := json.Unmarshal([]byte(nested), &list); err == nil {
			return list
		}
	}
	return nil
}

func ladder() []float64 {
	out := make([]float64, 0, model.MaxSpeedLevel+1)
	out = append(out, model.DefaultSpeedLadderMbps[:]...)
	return out
}
