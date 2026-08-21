package isp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

// GeoDatFileName is the asset written next to geoip.dat / geosite.dat in the
// xray bin folder. Routing rules reference it as
// `ext:gucci-isp.dat:!<CODE>` — the leading "!" inverts the match, so the rule
// fires for every source address that is *not* inside the selection.
const GeoDatFileName = "gucci-isp.dat"

// GroupCode returns the stable geoip code for an ISP selection. Clients that
// picked the same networks share one code (and therefore one routing rule),
// which keeps the generated config small no matter how many clients exist.
func GroupCode(ids []string) string {
	ids = Sanitize(ids)
	if len(ids) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return "ISPLOCK" + strings.ToUpper(hex.EncodeToString(sum[:5]))
}

func toCIDR(p netip.Prefix) *geodata.CIDR {
	addr := p.Addr()
	if addr.Is4In6() {
		addr = addr.Unmap()
		p = netip.PrefixFrom(addr, p.Bits()-96)
	}
	ip := addr.AsSlice()
	if len(ip) == 0 {
		return nil
	}
	bits := p.Bits()
	if bits < 0 {
		return nil
	}
	return &geodata.CIDR{Ip: ip, Prefix: uint32(bits)}
}

// BuildGeoDat serializes code -> prefix-set groups into xray's geoip.dat
// format. Groups without usable prefixes are skipped: an empty code would make
// an inverted rule match every address and black-hole the client entirely.
func BuildGeoDat(groups map[string][]netip.Prefix) ([]byte, error) {
	list := &geodata.GeoIPList{}
	codes := make([]string, 0, len(groups))
	for code := range groups {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		entry := &geodata.GeoIP{Code: strings.ToUpper(code)}
		for _, p := range groups[code] {
			if c := toCIDR(p); c != nil {
				entry.Cidr = append(entry.Cidr, c)
			}
		}
		if len(entry.Cidr) == 0 {
			continue
		}
		list.Entry = append(list.Entry, entry)
	}
	if len(list.Entry) == 0 {
		return nil, ErrNoGroups
	}
	return proto.Marshal(list)
}

// ErrNoGroups is returned when there is nothing to enforce.
var ErrNoGroups = errors.New("isp: no enforceable ISP groups")

// WriteGeoDat renders the groups and installs the asset atomically. The payload
// is parsed back before the rename, so a half-written or corrupt file can never
// reach xray — which would refuse to start with it.
func WriteGeoDat(dir string, groups map[string][]netip.Prefix) (string, error) {
	payload, err := BuildGeoDat(groups)
	if err != nil {
		return "", err
	}
	var check geodata.GeoIPList
	if err := proto.Unmarshal(payload, &check); err != nil {
		return "", err
	}
	if len(check.Entry) != len(groups) {
		// Not fatal on its own (empty groups are skipped), but the caller only
		// injects rules for codes it can find here.
		for code := range groups {
			found := false
			for _, e := range check.Entry {
				if strings.EqualFold(e.Code, code) {
					found = true
					break
				}
			}
			if !found {
				delete(groups, code)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, GeoDatFileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return final, nil
}

// GeoDatRef renders the routing-rule token for a group code. The "!" makes the
// rule match sources outside the allowed networks.
func GeoDatRef(code string) string {
	return "ext:" + GeoDatFileName + ":!" + strings.ToUpper(code)
}
