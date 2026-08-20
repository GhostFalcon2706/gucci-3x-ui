package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/isp"
	"github.com/mhsanaei/3x-ui/v3/internal/shaper"

	"gorm.io/gorm"
)

// allowedISPsColumnValue renders an ISP selection the same way GORM's
// `serializer:json` tag does, for the code paths that write the clients table
// through a raw column map instead of a struct.
func allowedISPsColumnValue(ids []string) string {
	ids = model.NormalizeAllowedISPs(ids)
	if len(ids) == 0 {
		return "null"
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "null"
	}
	return string(b)
}

// trafficMultipliersByEmail loads the accounting coefficient of the given
// clients. Emails missing from the clients table (or carrying the legacy 0)
// are reported as 1 so traffic is always counted at least 1:1 — a lookup
// failure must never make usage vanish.
func trafficMultipliersByEmail(tx *gorm.DB, emails []string) map[string]float64 {
	out := make(map[string]float64, len(emails))
	for _, email := range emails {
		out[email] = 1
	}
	if len(emails) == 0 {
		return out
	}
	if tx == nil {
		tx = database.GetDB()
	}
	if tx == nil {
		return out
	}
	type row struct {
		Email             string
		TrafficMultiplier float64
	}
	const chunk = 400
	for start := 0; start < len(emails); start += chunk {
		end := min(start+chunk, len(emails))
		var rows []row
		if err := tx.Model(&model.ClientRecord{}).
			Select("email", "traffic_multiplier").
			Where("email IN ?", emails[start:end]).
			Scan(&rows).Error; err != nil {
			// Keep the 1x defaults on error rather than dropping the tick.
			return out
		}
		for _, r := range rows {
			out[r.Email] = model.NormalizeTrafficMultiplier(r.TrafficMultiplier)
		}
	}
	return out
}

// SpeedLevelOption describes one entry of the speed ladder for the UI.
type SpeedLevelOption struct {
	Level int     `json:"level"`
	Mbps  float64 `json:"mbps"`
}

// ClientLimitsCapabilities is the payload behind GET /server/ispCatalog: what
// the client editor may offer and what this host can really enforce.
type ClientLimitsCapabilities struct {
	ISPs        []isp.ISP          `json:"isps"`
	SpeedLadder []SpeedLevelOption `json:"speedLadder"`
	Shaping     shaper.Capability  `json:"shaping"`
	Multiplier  struct {
		Min float64 `json:"min"`
		Max float64 `json:"max"`
	} `json:"multiplier"`
	ISPLock ispLockStatus `json:"ispLock"`
}

// GetClientLimitsCapabilities builds that payload.
func GetClientLimitsCapabilities() ClientLimitsCapabilities {
	out := ClientLimitsCapabilities{
		ISPs:    isp.Catalog(),
		Shaping: shaper.Detect(),
		ISPLock: ISPLockStatus(),
	}
	for level := 0; level <= model.MaxSpeedLevel; level++ {
		out.SpeedLadder = append(out.SpeedLadder, SpeedLevelOption{
			Level: level,
			Mbps:  model.DefaultSpeedLadderMbps[level],
		})
	}
	out.Multiplier.Min = model.MinTrafficMultiplier
	out.Multiplier.Max = model.MaxTrafficMultiplier
	return out
}
