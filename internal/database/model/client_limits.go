package model

import (
	"math"
	"slices"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/isp"
)

// MaxSpeedLevel is the slowest selectable tier. Level 0 means "unlimited" (the
// client gets the full line speed); every level above it is slower than the
// previous one, following the ladder in DefaultSpeedLadderMbps.
const MaxSpeedLevel = 8

// DefaultSpeedLadderMbps maps a speed level to its ceiling in megabits per
// second. Index 0 (unlimited) is 0 by definition. Panels can override the
// ladder in settings; this is the fallback used when nothing is configured.
var DefaultSpeedLadderMbps = [MaxSpeedLevel + 1]float64{0, 100, 50, 25, 10, 5, 2, 1, 0.5}

// MinTrafficMultiplier / MaxTrafficMultiplier bound the per-client accounting
// coefficient. The upper bound keeps a typo from burning a client's whole quota
// on the first megabyte; the lower bound keeps the counter moving.
const (
	MinTrafficMultiplier = 0.1
	MaxTrafficMultiplier = 100
)

// NormalizeSpeedLevel clamps a stored or user-supplied level into range.
func NormalizeSpeedLevel(level int) int {
	if level < 0 {
		return 0
	}
	if level > MaxSpeedLevel {
		return MaxSpeedLevel
	}
	return level
}

// SpeedLevelMbps returns the ceiling for a level using the default ladder.
// 0 means "no limit".
func SpeedLevelMbps(level int) float64 {
	return DefaultSpeedLadderMbps[NormalizeSpeedLevel(level)]
}

// NormalizeTrafficMultiplier clamps the accounting coefficient. Zero (the value
// every row written before this feature carries) and NaN both mean "unset" and
// normalize to 1, so existing clients keep counting traffic 1:1.
func NormalizeTrafficMultiplier(m float64) float64 {
	if m == 0 || math.IsNaN(m) || math.IsInf(m, 0) {
		return 1
	}
	if m < MinTrafficMultiplier {
		return MinTrafficMultiplier
	}
	if m > MaxTrafficMultiplier {
		return MaxTrafficMultiplier
	}
	// Keep two decimals so 1.005 and 1.0049999 can't produce different configs.
	return math.Round(m*100) / 100
}

// NormalizeAllowedISPs canonicalizes an ISP selection: unknown ids are dropped,
// "all" (or a selection covering every enforceable network) collapses to nil,
// and the result is sorted so equal selections compare equal.
func NormalizeAllowedISPs(ids []string) []string {
	return isp.Sanitize(ids)
}

// ApplyTrafficMultiplier scales a byte delta by the client's coefficient,
// rounding half up and never turning a non-zero delta into zero (a 0.1x
// multiplier on a 5-byte delta still has to count something).
func ApplyTrafficMultiplier(delta int64, multiplier float64) int64 {
	if delta == 0 {
		return 0
	}
	m := NormalizeTrafficMultiplier(multiplier)
	if m == 1 {
		return delta
	}
	scaled := math.Round(float64(delta) * m)
	if scaled > math.MaxInt64 {
		return math.MaxInt64
	}
	if scaled < math.MinInt64 {
		return math.MinInt64
	}
	out := int64(scaled)
	if out == 0 {
		if delta > 0 {
			return 1
		}
		return -1
	}
	return out
}

// MultiplierSuffix renders the tag appended to a config's display name, e.g.
// " (x2)" for a double-charging client. A 1x client gets no suffix.
func MultiplierSuffix(multiplier float64) string {
	m := NormalizeTrafficMultiplier(multiplier)
	if m == 1 {
		return ""
	}
	return " (x" + trimFloat(m) + ")"
}

// trimFloat renders a multiplier without trailing zeros: 2 -> "2", 1.5 -> "1.5".
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// SameAs reports whether two client rows carry identical data. It exists
// because ClientRecord stopped being comparable with == once AllowedISPs (a
// slice) was added: the slice is compared element-wise and the rest of the
// struct is compared by value.
func (r ClientRecord) SameAs(other ClientRecord) bool {
	if !slices.Equal(r.AllowedISPs, other.AllowedISPs) {
		return false
	}
	type recordKey = struct {
		Id                int
		Email             string
		SubID             string
		UUID              string
		Password          string
		Auth              string
		Flow              string
		Security          string
		Reverse           string
		PrivateKey        string
		PublicKey         string
		AllowedIPs        string
		PreSharedKey      string
		KeepAlive         int
		Secret            string
		AdTag             string
		LimitIP           int
		TotalGB           int64
		ExpiryTime        int64
		Enable            bool
		TgID              int64
		Group             string
		Comment           string
		Reset             int
		SpeedLevel        int
		TrafficMultiplier float64
		CreatedAt         int64
		UpdatedAt         int64
	}
	flatten := func(v ClientRecord) recordKey {
		return recordKey{
			Id: v.Id, Email: v.Email, SubID: v.SubID, UUID: v.UUID, Password: v.Password,
			Auth: v.Auth, Flow: v.Flow, Security: v.Security, Reverse: v.Reverse,
			PrivateKey: v.PrivateKey, PublicKey: v.PublicKey, AllowedIPs: v.AllowedIPs,
			PreSharedKey: v.PreSharedKey, KeepAlive: v.KeepAlive, Secret: v.Secret,
			AdTag: v.AdTag, LimitIP: v.LimitIP, TotalGB: v.TotalGB, ExpiryTime: v.ExpiryTime,
			Enable: v.Enable, TgID: v.TgID, Group: v.Group, Comment: v.Comment, Reset: v.Reset,
			SpeedLevel: v.SpeedLevel, TrafficMultiplier: v.TrafficMultiplier,
			CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		}
	}
	return flatten(r) == flatten(other)
}
