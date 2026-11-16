package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Spec agent-usability-1 items B, C and E over MCP.

const (
	auHostA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	auHostB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	auHostC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func auServiceRow(host, hostName, entry string, gone bool) map[string]any {
	row := map[string]any{"id": "s-" + host[:4], "host_id": host, "host_name": hostName, "project_id": vrProject, "catalog_entry": entry, "gone_at": nil}
	if gone {
		row["gone_at"] = "2026-11-16T10:00:00Z"
	}
	return row
}

// entryStatusReply serves two pages of project services, then a program
// status for any host.
func entryStatusReply(t *testing.T, page1, page2 []any) func(*http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/projects/"+vrProject+"/services" {
			q := r.URL.Query()
			if q.Get("view") != "deploybot" || q.Get("limit") != "200" {
				t.Errorf("services query %s", r.URL.RawQuery)
			}
			payload := map[string]any{"items": page1, "next_cursor": "page-2", "total": len(page1) + len(page2)}
			if q.Get("cursor") == "page-2" {
				payload = map[string]any{"items": page2, "next_cursor": nil, "total": len(page1) + len(page2)}
			}
			raw, _ := json.Marshal(map[string]any{"code": "ok", "msg": "ok", "payload": payload})
			return http.StatusOK, string(raw)
		}
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"unit":"u","active_state":"active","sub_state":"running","main_pid":7}}`
	}
}

// TestEntryStatusInfersHost: one host among the live copies is used; gone
// copies, other entries and other projects do not count.
func TestEntryStatusInfersHost(t *testing.T) {
	other := auServiceRow(auHostC, "pi9", "web", false)
	other["project_id"] = "22222222-2222-4222-8222-222222222222"
	cs, seen := vrSession(t, nil, entryStatusReply(t,
		[]any{auServiceRow(auHostA, "pi1", "web", false), auServiceRow(auHostB, "pi2", "web", true), other},
		[]any{auServiceRow(auHostA, "pi1", "web", false), auServiceRow(auHostB, "pi2", "api", false)},
	))
	res, text, _ := callVR(t, cs, "entry_status", map[string]any{"project_id": vrProject, "entry": "web"})
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	calls := seen()
	last := calls[len(calls)-1]
	if len(calls) != 3 || last.Method != http.MethodPost || last.Path != "/v1/projects/"+vrProject+"/catalog/web/status" || last.Body != `{"host_id":"`+auHostA+`"}` {
		t.Fatalf("calls %#v", calls)
	}
	// With host_id the services list is not read.
	before := len(seen())
	if res, text, _ := callVR(t, cs, "entry_status", map[string]any{"project_id": vrProject, "entry": "web", "host_id": auHostB}); res.IsError {
		t.Fatalf("explicit host %s", text)
	}
	if calls := seen(); len(calls) != before+1 || calls[before].Body != `{"host_id":"`+auHostB+`"}` {
		t.Fatalf("explicit calls %#v", calls[before:])
	}
}

// TestEntryStatusAsksForHost: several hosts are listed with names and ids;
// none asks for host_id. No status call is made.
func TestEntryStatusAsksForHost(t *testing.T) {
	cs, seen := vrSession(t, nil, entryStatusReply(t,
		[]any{auServiceRow(auHostB, "pi2", "web", false)},
		[]any{auServiceRow(auHostA, "pi1", "web", false)},
	))
	res, text, _ := callVR(t, cs, "entry_status", map[string]any{"project_id": vrProject, "entry": "web"})
	want := "bad_request: web runs on 2 hosts: pi1 (" + auHostA + "), pi2 (" + auHostB + "); pass host_id"
	if !res.IsError || text != want {
		t.Fatalf("several %q", text)
	}
	cs, seen = vrSession(t, nil, entryStatusReply(t, []any{auServiceRow(auHostA, "pi1", "web", true)}, nil))
	res, text, _ = callVR(t, cs, "entry_status", map[string]any{"project_id": vrProject, "entry": "web"})
	if !res.IsError || text != "bad_request: no running copy of web was found in this project; pass host_id (list_hosts lists the project's hosts)" {
		t.Fatalf("none %q", text)
	}
	for _, c := range seen() {
		if c.Method == http.MethodPost {
			t.Fatalf("status called %#v", c)
		}
	}
}

const auReadiness = `{"code":"ok","msg":"ok","payload":{"ready":false,"checks":[` +
	`{"check":"env_sets","status":"missing","message":"env set dd does not exist","fix":{"action":"create_env_set","set":"dd"}},` +
	`{"check":"registry","status":"ok","message":"public image, no login needed","fix":{"action":"none"}}]}}`

// TestCheckReadinessSpec: spec goes to the POST route with host_id; entry
// keeps the GET route; exactly one of the two.
func TestCheckReadinessSpec(t *testing.T) {
	cs, seen := vrSession(t, nil, func(r *http.Request) (int, string) { return http.StatusOK, auReadiness })
	spec := map[string]any{"kind": "service", "image": "ghcr.io/joe/dd:v1", "env_sets": []any{"dd"}}
	res, text, out := callVR(t, cs, "check_readiness", map[string]any{"project_id": vrProject, "spec": spec, "host_id": auHostA})
	if res.IsError {
		t.Fatalf("spec %s", text)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/projects/"+vrProject+"/catalog/readiness" {
		t.Fatalf("calls %#v", calls)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil || body["host_id"] != auHostA || len(body) != 2 {
		t.Fatalf("body %s", calls[0].Body)
	}
	if !strings.HasPrefix(text, "This spec is not ready: 1 check(s) missing.") || !strings.Contains(text, "call create_env_set to create env set dd") || out["ready"] != false {
		t.Fatalf("summary %q %v", text, out)
	}
	// Entry mode is unchanged.
	if res, text, _ := callVR(t, cs, "check_readiness", map[string]any{"project_id": vrProject, "entry": "web"}); res.IsError {
		t.Fatalf("entry %s", text)
	}
	if calls := seen(); calls[1].Method != http.MethodGet || calls[1].Path != "/v1/projects/"+vrProject+"/catalog/web/readiness" {
		t.Fatalf("entry call %#v", calls[1])
	}
	// Both, neither, or a spec with an unknown field: refused before the API.
	before := len(seen())
	for _, args := range []map[string]any{
		{"project_id": vrProject},
		{"project_id": vrProject, "entry": "web", "spec": spec},
	} {
		res, text, _ := callVR(t, cs, "check_readiness", args)
		if !res.IsError || text != "bad_request: pass exactly one of entry or spec" {
			t.Fatalf("%v: %q", args, text)
		}
	}
	if res, _, _ := callVR(t, cs, "check_readiness", map[string]any{"project_id": vrProject, "spec": map[string]any{"image": "x", "bogus": 1}}); !res.IsError {
		t.Fatal("unknown spec field accepted")
	}
	if len(seen()) != before {
		t.Fatal("refused input reached the API")
	}
}

// TestConfirmPassesStateTexts: the API's expired and used texts reach the
// agent unchanged (api-v1 v1.32 item 1).
func TestConfirmPassesStateTexts(t *testing.T) {
	for _, msg := range []string{
		"this confirmation expired; call the tool again for a new one",
		"this confirmation was already used",
	} {
		cs, _ := vrSession(t, nil, func(r *http.Request) (int, string) {
			return http.StatusConflict, `{"code":"conflict","msg":"` + msg + `","payload":null}`
		})
		res, text, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": vrID, "idempotency_key": "k-1"})
		if !res.IsError || text != "conflict: "+msg {
			t.Fatalf("confirm %q", text)
		}
	}
}

// TestEntryStatusTruncatedList: after 5 full pages a cursor is still set,
// so no host is inferred, whether the pages held one host or none.
func TestEntryStatusTruncatedList(t *testing.T) {
	for name, entry := range map[string]string{"one host": "web", "no host": "other"} {
		t.Run(name, func(t *testing.T) {
			page := make([]any, 200)
			for i := range page {
				page[i] = auServiceRow(auHostA, "pi1", "web", false)
			}
			cs, seen := vrSession(t, nil, func(r *http.Request) (int, string) {
				raw, _ := json.Marshal(map[string]any{"code": "ok", "msg": "ok", "payload": map[string]any{
					"items": page, "next_cursor": "more", "total": 5000,
				}})
				return http.StatusOK, string(raw)
			})
			res, text, _ := callVR(t, cs, "entry_status", map[string]any{"project_id": vrProject, "entry": entry})
			if !res.IsError || text != "bad_request: this project has too many services to pick a host automatically; pass host_id (list_services with project_id shows where the entry runs)" {
				t.Fatalf("text %q", text)
			}
			calls := seen()
			if len(calls) != 5 {
				t.Fatalf("pages read %d", len(calls))
			}
			for _, c := range calls {
				if c.Method != http.MethodGet {
					t.Fatalf("status called %#v", c)
				}
			}
		})
	}
}
