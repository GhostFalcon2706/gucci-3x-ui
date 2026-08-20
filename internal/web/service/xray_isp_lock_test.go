package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/isp"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func decodeRouting(t *testing.T, cfg *xray.Config) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(cfg.RouterConfig) == 0 {
		return out
	}
	if err := json.Unmarshal(cfg.RouterConfig, &out); err != nil {
		t.Fatalf("routing section is not valid JSON: %v", err)
	}
	return out
}

func newTestConfig() *xray.Config {
	return &xray.Config{
		RouterConfig:   json_util.RawMessage(`{"rules":[{"type":"field","outboundTag":"direct","network":"tcp,udp"}]}`),
		OutboundConfigs: json_util.RawMessage(`[{"tag":"direct","protocol":"freedom"}]`),
	}
}

func TestInjectISPLocksAddsInvertedRuleAndBlackhole(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	cfg := newTestConfig()

	injectISPLocks(cfg, []ispLockEntry{
		{email: "a@example.com", ids: []string{"mci"}},
		{email: "b@example.com", ids: []string{"mci"}},
		{email: "c@example.com", ids: []string{"irancell", "shatel"}},
	})

	routing := decodeRouting(t, cfg)
	rules, _ := routing["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("expected 2 injected rules + 1 original, got %d", len(rules))
	}

	// Clients sharing a selection must share a single rule.
	first, _ := rules[0].(map[string]any)
	users, _ := first["user"].([]any)
	source, _ := first["source"].([]any)
	if first["outboundTag"] != ISPBlockOutboundTag {
		t.Fatalf("injected rule must target the blackhole, got %v", first["outboundTag"])
	}
	if len(source) != 1 {
		t.Fatalf("expected exactly one source token, got %v", source)
	}
	token, _ := source[0].(string)
	if got := token[:len("ext:"+isp.GeoDatFileName+":!")]; got != "ext:"+isp.GeoDatFileName+":!" {
		t.Fatalf("source token must be an inverted geo reference, got %q", token)
	}
	if len(users) == 0 {
		t.Fatal("rule must carry the locked emails")
	}

	// The original rule must survive, after the injected ones.
	last, _ := rules[len(rules)-1].(map[string]any)
	if last["outboundTag"] != "direct" {
		t.Fatalf("operator rules must be preserved, got %v", last)
	}

	var outbounds []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatalf("outbounds are not valid JSON: %v", err)
	}
	if len(outbounds) != 2 || outbounds[1]["protocol"] != "blackhole" {
		t.Fatalf("a blackhole outbound must be appended, got %v", outbounds)
	}

	// The referenced asset has to exist, otherwise xray refuses to start.
	if _, err := os.Stat(os.Getenv("XUI_BIN_FOLDER") + "/" + isp.GeoDatFileName); err != nil {
		t.Fatalf("geo asset was not written: %v", err)
	}

	status := ISPLockStatus()
	if status.Groups != 2 || status.Clients != 3 {
		t.Fatalf("unexpected status %+v", status)
	}
}

func TestInjectISPLocksNoopWithoutRestrictions(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	cfg := newTestConfig()
	before := string(cfg.RouterConfig)

	injectISPLocks(cfg, nil)
	injectISPLocks(cfg, []ispLockEntry{{email: "a@example.com", ids: []string{"all"}}})
	injectISPLocks(cfg, []ispLockEntry{{email: "a@example.com", ids: []string{"does-not-exist"}}})

	if string(cfg.RouterConfig) != before {
		t.Fatalf("config must stay untouched, got %s", cfg.RouterConfig)
	}
}

func TestInjectISPLocksSkipsISPsWithoutPrefixes(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	cfg := newTestConfig()
	before := string(cfg.RouterConfig)

	// ariantel is a catalog entry that announces no address space: locking a
	// client to it must be reported as skipped instead of black-holing them.
	injectISPLocks(cfg, []ispLockEntry{{email: "a@example.com", ids: []string{"ariantel"}}})

	if string(cfg.RouterConfig) != before {
		t.Fatalf("unenforceable lock must not change routing, got %s", cfg.RouterConfig)
	}
	if status := ISPLockStatus(); len(status.Skipped) != 1 || status.Skipped[0] != "a@example.com" {
		t.Fatalf("unenforceable lock must be reported, got %+v", status)
	}
}
