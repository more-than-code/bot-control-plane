package agentws

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseProgramsFiles(t *testing.T) {
	absent, ok := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true}`))
	if !ok || absent == nil || absent.Files {
		t.Fatalf("absent files %#v %v", absent, ok)
	}
	on, ok := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[],"allow_shell":false,"policy_hash":"abc","images":false,"docker":false,"files":true}`))
	if !ok || on == nil || !on.Files || on.RunAs == nil {
		t.Fatalf("files true %#v %v", on, ok)
	}
	if _, ok := parsePrograms(json.RawMessage(`{"enabled":true,"files":"yes"}`)); ok {
		t.Fatal("non-bool files accepted")
	}
}

func TestParseProgramsDockerHealth(t *testing.T) {
	base := `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true}`
	absent, ok := parsePrograms(json.RawMessage(base))
	if !ok || absent == nil || absent.DockerHealth || absent.Healthcheck || !absent.Docker {
		t.Fatalf("absent %#v %v", absent, ok)
	}
	on, ok := parsePrograms(json.RawMessage(`{"enabled":true,"run_as":[],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true,"docker_health":true,"healthcheck":true}`))
	if !ok || on == nil || !on.DockerHealth || !on.Healthcheck {
		t.Fatalf("flags %#v %v", on, ok)
	}
	if _, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"docker_health":"yes"}`)); ok {
		t.Fatal("non-bool docker_health accepted")
	}
	if _, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"healthcheck":1}`)); ok {
		t.Fatal("non-bool healthcheck accepted")
	}
}

func TestParseProgramsLogging(t *testing.T) {
	base := `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true}`
	absent, ok := parsePrograms(json.RawMessage(base))
	if !ok || absent == nil || absent.Logging {
		t.Fatalf("absent %#v %v", absent, ok)
	}
	on, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"logging":true}`))
	if !ok || on == nil || !on.Logging {
		t.Fatalf("logging true %#v %v", on, ok)
	}
	off, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"logging":false}`))
	if !ok || off == nil || off.Logging {
		t.Fatalf("logging false %#v %v", off, ok)
	}
	if _, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"logging":1}`)); ok {
		t.Fatal("non-bool logging accepted")
	}
	if _, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"logging":"yes"}`)); ok {
		t.Fatal("string logging accepted")
	}
}

func TestPolicyDeniedMapping(t *testing.T) {
	// api-v1 v1.23 item 3: a policy refusal names runner.toml, never the agent's reason.
	const want = "the host refused this under its policy (runner.toml)"
	code, msg := rejectCode("policy_denied")
	if code != "forbidden" || msg != want {
		t.Fatalf("reject %s %s", code, msg)
	}
	denied := failedTask("policy_denied", json.RawMessage(`{"lines":["secret"]}`))
	if denied.Code != "forbidden" || denied.Msg != want || denied.Payload != nil {
		t.Fatalf("failed policy %#v", denied)
	}
	failed := failedTask("start_failed", json.RawMessage(`{"lines":["boom"]}`))
	if failed.Code != "error" || failed.Msg != "start_failed" || string(failed.Payload) != `{"lines":["boom"]}` {
		t.Fatalf("start_failed %#v", failed)
	}
	for _, tc := range []struct{ in, code, msg string }{
		{"forbidden", "forbidden", "forbidden"},
		{"unknown_service", "not_found", "not found"},
		{"bad_spec", "bad_request", "bad request"},
		{"busy", "unavailable", "host unavailable"},
		{"other", "error", "internal error"},
	} {
		got, text := rejectCode(tc.in)
		if got != tc.code || text != tc.msg {
			t.Fatalf("%s → %s %s", tc.in, got, text)
		}
	}
}

// api-v1 v1.22 item 5 and v1.23 item 1: secrets and projects from hello.
func TestParseProgramsSecretsProjects(t *testing.T) {
	base := `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true}`
	old, ok := parsePrograms(json.RawMessage(base))
	if !ok || old == nil || old.Secrets || old.Projects != nil || old.ProjectListed != nil {
		t.Fatalf("older agent %#v %v", old, ok)
	}
	on, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"secrets":true,"projects":["home-lab","acme"]}`))
	if !ok || on == nil || !on.Secrets || len(on.Projects) != 2 || on.Projects[0] != "home-lab" {
		t.Fatalf("new agent %#v %v", on, ok)
	}
	empty, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + `,"projects":[]}`))
	if !ok || empty.Projects == nil || len(empty.Projects) != 0 {
		t.Fatalf("empty list must stay known %#v %v", empty, ok)
	}
	many := `"p0"`
	for i := 1; i < 33; i++ {
		many += `,"p"`
	}
	for _, bad := range []string{
		`,"secrets":"yes"}`,
		`,"projects":"home-lab"}`,
		`,"projects":[1]}`,
		`,"projects":[""]}`,
		`,"projects":[` + many + `]}`,
		`,"project_listed":true}`,
		`,"project_listed":null}`,
	} {
		if _, ok := parsePrograms(json.RawMessage(base[:len(base)-1] + bad)); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// agent-protocol v1.4 slug grammar and the v1.12 item 5 cap of 32.
func TestParseProgramsProjectsBoundaries(t *testing.T) {
	base := `{"enabled":true,"run_as":["joe"],"allow_shell":false,"policy_hash":"abc","images":true,"docker":true`
	list := func(n int) string {
		out := ""
		for i := 0; i < n; i++ {
			if i > 0 {
				out += ","
			}
			out += `"p` + string(rune('a'+i/26)) + string(rune('a'+i%26)) + `"`
		}
		return out
	}
	if p, ok := parsePrograms(json.RawMessage(base + `,"projects":[` + list(32) + `]}`)); !ok || len(p.Projects) != 32 {
		t.Fatalf("32 slugs refused %v", ok)
	}
	if _, ok := parsePrograms(json.RawMessage(base + `,"projects":[` + list(33) + `]}`)); ok {
		t.Fatal("33 slugs accepted")
	}
	long := "a" + strings.Repeat("b-", 31) + "c" // 64 characters, valid
	if len(long) != 64 {
		t.Fatalf("len %d", len(long))
	}
	if p, ok := parsePrograms(json.RawMessage(base + `,"projects":["` + long + `"]}`)); !ok || p.Projects[0] != long {
		t.Fatalf("64-character slug refused %v", ok)
	}
	for _, bad := range []string{long + "d", "-lead", "Upper", "under_score", "dot.ted", ""} {
		if _, ok := parsePrograms(json.RawMessage(base + `,"projects":["` + bad + `"]}`)); ok {
			t.Fatalf("slug %q accepted", bad)
		}
	}
}
