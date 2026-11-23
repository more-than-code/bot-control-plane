package agentws

import (
	"encoding/json"
	"testing"
)

// agent-protocol v1.20 item 2: programs.agent_upgrade is a known hello
// field, and last_upgrade has a strict shape.
func TestHelloAgentUpgradeFlag(t *testing.T) {
	p, ok := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[],"agent_upgrade":true}`))
	if !ok || p == nil || !p.AgentUpgrade {
		t.Fatalf("programs %+v %v", p, ok)
	}
	p, ok = parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[]}`))
	if !ok || p.AgentUpgrade {
		t.Fatalf("absent flag %+v %v", p, ok)
	}
}

func TestParseUpgradeResult(t *testing.T) {
	for _, raw := range []string{``, `null`} {
		if res, ok := parseUpgradeResult(json.RawMessage(raw)); !ok || res != nil {
			t.Fatalf("%q: %+v %v", raw, res, ok)
		}
	}
	res, ok := parseUpgradeResult(json.RawMessage(`{"version":"0.2.16","outcome":"rolled_back","reason":"not_reconnected","at":"2026-11-23T10:00:00Z"}`))
	if !ok || res.Version != "0.2.16" || res.Outcome != "rolled_back" || res.Reason != "not_reconnected" || res.At.IsZero() {
		t.Fatalf("good %+v %v", res, ok)
	}
	// An odd reason is dropped, not echoed.
	res, ok = parseUpgradeResult(json.RawMessage(`{"version":"0.2.16","outcome":"failed","reason":"Bad Reason!","at":"2026-11-23T10:00:00Z"}`))
	if !ok || res.Reason != "" {
		t.Fatalf("reason %+v %v", res, ok)
	}
	for _, bad := range []string{
		`"x"`,
		`{"version":"v0.2.16","outcome":"succeeded","reason":"","at":"2026-11-23T10:00:00Z"}`,
		`{"version":"0.2.16","outcome":"running","reason":"","at":"2026-11-23T10:00:00Z"}`,
		`{"version":"0.2.16","outcome":"succeeded","reason":"","at":"yesterday"}`,
	} {
		if _, ok := parseUpgradeResult(json.RawMessage(bad)); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
}
