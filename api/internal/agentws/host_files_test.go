package agentws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// agent-protocol v1.18 item 3: hello.programs.host_files is accepted and
// checked; a bad list makes the programs object unusable like any other
// bad hello field.
func TestParseProgramsHostFiles(t *testing.T) {
	ok := `{"enabled":true,"run_as":[],"host_files":[{"name":"dd-policy","path":"/etc/dd-worker/policy.toml","secret":false,"unit":"dd-worker.service","health":true}]}`
	p, good := parsePrograms(json.RawMessage(ok))
	if !good || p == nil || len(p.HostFiles) != 1 || p.HostFiles[0].Unit != "dd-worker.service" || !p.HostFiles[0].Health {
		t.Fatalf("parse %+v %v", p, good)
	}
	if p, good := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[],"host_files":[]}`)); !good || p.HostFiles == nil || len(p.HostFiles) != 0 {
		t.Fatalf("empty list %+v", p)
	}
	if p, good := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[]}`)); !good || p.HostFiles != nil {
		t.Fatalf("absent %+v", p)
	}
	entry := func(name, path, unit, extra string) string {
		return `{"enabled":true,"run_as":[],"host_files":[{"name":"` + name + `","path":"` + path + `","secret":true,"unit":"` + unit + `","health":false` + extra + `}]}`
	}
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = `{"name":"f` + string(rune('a'+i)) + `","path":"/etc/d/f` + string(rune('a'+i)) + `","secret":false,"unit":"u.service","health":false}`
	}
	if _, good := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[],"host_files":[` + strings.Join(nine[:8], ",") + `]}`)); !good {
		t.Fatal("8 files refused")
	}
	if _, good := parsePrograms(json.RawMessage(entry("ok", "/etc/"+strings.Repeat("d", 128)+"/"+strings.Repeat("f", 128), "u.service", ""))); !good {
		t.Fatal("128-byte components refused")
	}
	for _, bad := range []string{
		entry("Bad", "/etc/d/f", "u.service", ""),
		entry("ok", "/etc/d/f/g", "u.service", ""),
		entry("ok", "/var/d/f", "u.service", ""),
		entry("ok", "/etc/../f", "u.service", ""),
		entry("ok", "/etc/./f", "u.service", ""),
		entry("ok", "/etc/d/.", "u.service", ""),
		entry("ok", "/etc/d/..", "u.service", ""),
		entry("ok", "/etc//f", "u.service", ""),
		entry("ok", "/etc/d/", "u.service", ""),
		entry("ok", "/etc/d/"+strings.Repeat("f", 129), "u.service", ""),
		entry("ok", "etc/d/f", "u.service", ""),
		entry("ok", "/etc/d/f", "u", ""),
		entry("ok", "/etc/d/f", "u.service", `,"content":"x"`),
		`{"enabled":true,"run_as":[],"host_files":[` + strings.Join(nine, ",") + `]}`,
		`{"enabled":true,"run_as":[],"host_files":[` + nine[0] + `,` + nine[0] + `]}`,
	} {
		if _, good := parsePrograms(json.RawMessage(bad)); good {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// host_files_status frames: 1 per second, burst 5; the excess is dropped.
func TestAllowStatus(t *testing.T) {
	ac := &agentConn{}
	at := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	for i := range 5 {
		if !ac.allowStatus(at) {
			t.Fatalf("frame %d refused", i)
		}
	}
	if ac.allowStatus(at) {
		t.Fatal("sixth frame allowed")
	}
	if !ac.allowStatus(at.Add(time.Second)) || ac.allowStatus(at.Add(time.Second)) {
		t.Fatal("refill is not one a second")
	}
}
