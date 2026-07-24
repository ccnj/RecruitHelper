package main

import (
	"testing"
	"time"

	"recruithelper/client/service/internal/session"
)

func TestProductPluginRuntimeSelectsLatestOnlineHandWithoutExposingIdentity(t *testing.T) {
	base := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	online, health, version, match := productPluginRuntime([]session.HandState{
		{
			HandID: "historical-offline", Online: false, Health: session.HealthOffline,
			ExtVersion: "9.9.9", ContractMatch: false, SessionAt: base.Add(2 * time.Hour),
		},
		{
			HandID: "current", Online: true, Health: session.HealthReady,
			ExtVersion: "1.2.3", ContractMatch: true, SessionAt: base,
		},
	})
	if !online || health != "ready" || version != "1.2.3" || !match {
		t.Fatalf("online=%v health=%q version=%q match=%v", online, health, version, match)
	}
}

func TestProductPluginRuntimeTreatsStalledAndMissingHandsAsOffline(t *testing.T) {
	online, health, version, match := productPluginRuntime([]session.HandState{{
		Online: true, Health: session.HealthStalled, ExtVersion: "1.2.3", ContractMatch: true,
	}})
	if online || health != "stalled" || version != "1.2.3" || !match {
		t.Fatalf("stalled snapshot online=%v health=%q version=%q match=%v", online, health, version, match)
	}

	online, health, version, match = productPluginRuntime(nil)
	if online || health != "offline" || version != "" || match {
		t.Fatalf("empty snapshot online=%v health=%q version=%q match=%v", online, health, version, match)
	}
}
