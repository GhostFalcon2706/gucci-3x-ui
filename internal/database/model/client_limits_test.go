package model

import (
	"net/netip"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/isp"
)

func TestNormalizeTrafficMultiplier(t *testing.T) {
	cases := map[float64]float64{
		0: 1, 1: 1, 2: 2, 0.5: 0.5,
		-3: MinTrafficMultiplier, 1e9: MaxTrafficMultiplier, 1.006: 1.01, 2.567: 2.57,
	}
	for in, want := range cases {
		if got := NormalizeTrafficMultiplier(in); got != want {
			t.Errorf("NormalizeTrafficMultiplier(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestApplyTrafficMultiplier(t *testing.T) {
	if got := ApplyTrafficMultiplier(20<<20, 2); got != 40<<20 {
		t.Errorf("20MiB at x2 = %d, want %d", got, 40<<20)
	}
	if got := ApplyTrafficMultiplier(1000, 0); got != 1000 {
		t.Errorf("unset multiplier must count 1:1, got %d", got)
	}
	if got := ApplyTrafficMultiplier(5, 0.1); got != 1 {
		t.Errorf("small delta must never round to zero, got %d", got)
	}
}

func TestMultiplierSuffix(t *testing.T) {
	if s := MultiplierSuffix(1); s != "" {
		t.Errorf("1x must have no suffix, got %q", s)
	}
	if s := MultiplierSuffix(2); s != " (x2)" {
		t.Errorf("got %q", s)
	}
	if s := MultiplierSuffix(1.5); s != " (x1.5)" {
		t.Errorf("got %q", s)
	}
}

func TestNormalizeSpeedLevel(t *testing.T) {
	if NormalizeSpeedLevel(-1) != 0 || NormalizeSpeedLevel(99) != MaxSpeedLevel {
		t.Fatal("speed level must clamp into range")
	}
	for lvl := 1; lvl < MaxSpeedLevel; lvl++ {
		if SpeedLevelMbps(lvl) <= SpeedLevelMbps(lvl+1) {
			t.Fatalf("level %d must be faster than level %d", lvl, lvl+1)
		}
	}
	if SpeedLevelMbps(0) != 0 {
		t.Fatal("level 0 must mean unlimited")
	}
}

func TestNormalizeAllowedISPs(t *testing.T) {
	if got := NormalizeAllowedISPs([]string{"all", "mci"}); got != nil {
		t.Errorf(`"all" must collapse to no restriction, got %v`, got)
	}
	if got := NormalizeAllowedISPs([]string{"MCI", " mci ", "bogus"}); len(got) != 1 || got[0] != "mci" {
		t.Errorf("got %v", got)
	}
}

func TestISPPrefixMatching(t *testing.T) {
	mci, ok := isp.Get("mci")
	if !ok || !mci.Enforceable() {
		t.Fatal("MCI must ship with announced prefixes")
	}
	prefixes := isp.Prefixes([]string{"mci"})
	inside := prefixes[0].Addr()
	if !isp.Allows([]string{"mci"}, inside) {
		t.Fatalf("%v must be allowed for MCI", inside)
	}
	outside := netip.MustParseAddr("8.8.8.8")
	if isp.Allows([]string{"mci"}, outside) {
		t.Fatal("8.8.8.8 must not be inside MCI ranges")
	}
	if !isp.Allows(nil, outside) {
		t.Fatal("an empty selection must allow everything")
	}
}
