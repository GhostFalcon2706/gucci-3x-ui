package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// TestClientLimitsRoundTrip walks the three new per-client limits through the
// real persistence path: inbound settings JSON -> SyncInbound -> clients table
// -> ToClient, and then checks that traffic accounting charges the configured
// multiplier.
func TestClientLimitsRoundTrip(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()
	const email = "limits@example.com"

	settings, _ := json.Marshal(map[string]any{"clients": []map[string]any{{
		"email":             email,
		"id":                "0d2f7c1a-3f6c-4a2b-9a8f-2c1d4e5f6a7b",
		"enable":            true,
		"subId":             "sub-limits",
		"speedLevel":        3,
		"trafficMultiplier": 2,
		"allowedIsps":       []string{"mci", "irancell"},
	}}})

	inbound := &model.Inbound{
		Tag: "vless-limits", Enable: true, Port: 51001, Protocol: model.VLESS,
		StreamSettings: `{"network":"tcp","security":"none"}`, Settings: string(settings),
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	inboundSvc := InboundService{}
	clientSvc := ClientService{}
	clients, err := inboundSvc.GetClients(inbound)
	if err != nil {
		t.Fatalf("GetClients: %v", err)
	}
	if clients[0].SpeedLevel != 3 || clients[0].TrafficMultiplier != 2 || len(clients[0].AllowedISPs) != 2 {
		t.Fatalf("settings JSON did not carry the limits: %+v", clients[0])
	}
	if err := clientSvc.SyncInbound(nil, inbound.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	var stored model.ClientRecord
	if err := db.Where("email = ?", email).First(&stored).Error; err != nil {
		t.Fatalf("load client record: %v", err)
	}
	if stored.SpeedLevel != 3 {
		t.Errorf("speed level = %d, want 3", stored.SpeedLevel)
	}
	if stored.TrafficMultiplier != 2 {
		t.Errorf("traffic multiplier = %v, want 2", stored.TrafficMultiplier)
	}
	if len(stored.AllowedISPs) != 2 || stored.AllowedISPs[0] != "irancell" || stored.AllowedISPs[1] != "mci" {
		t.Errorf("allowed ISPs = %v, want the sorted pair", stored.AllowedISPs)
	}

	// A client with no explicit multiplier keeps counting 1:1.
	plain := model.ClientRecord{Email: "plain@example.com", Enable: true}
	if err := db.Create(&plain).Error; err != nil {
		t.Fatalf("create plain client: %v", err)
	}

	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, InboundId: inbound.Id}).Error; err != nil {
		t.Fatalf("seed traffic row: %v", err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: plain.Email, Enable: true, InboundId: inbound.Id}).Error; err != nil {
		t.Fatalf("seed plain traffic row: %v", err)
	}

	const oneMiB = int64(1 << 20)
	if _, _, err := inboundSvc.AddTraffic(nil, []*xray.ClientTraffic{
		{Email: email, Up: oneMiB, Down: 2 * oneMiB},
		{Email: plain.Email, Up: oneMiB, Down: 2 * oneMiB},
	}); err != nil {
		t.Fatalf("AddTraffic: %v", err)
	}

	var charged xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&charged).Error; err != nil {
		t.Fatalf("reload traffic: %v", err)
	}
	if charged.Up != 2*oneMiB || charged.Down != 4*oneMiB {
		t.Errorf("x2 client charged up=%d down=%d, want %d/%d", charged.Up, charged.Down, 2*oneMiB, 4*oneMiB)
	}

	var untouched xray.ClientTraffic
	if err := db.Where("email = ?", plain.Email).First(&untouched).Error; err != nil {
		t.Fatalf("reload plain traffic: %v", err)
	}
	if untouched.Up != oneMiB || untouched.Down != 2*oneMiB {
		t.Errorf("1x client charged up=%d down=%d, want %d/%d", untouched.Up, untouched.Down, oneMiB, 2*oneMiB)
	}
}
