package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.48 item 8: upgrade_agent plans in memory, confirm posts.

const uaHost = "5bdc639e-8430-4c0c-b2b2-031b56272c15"

func uaHostBody(allowed, update bool, state string) string {
	last := "null"
	if state != "" {
		last = `{"id":"35749389-eb52-4e3a-9e82-2cf8807dbd8c","to":"0.2.15","state":"` + state + `"}`
	}
	return `{"code":"ok","msg":"ok","payload":{"id":"` + uaHost + `","name":"pi3","agent_version":"0.2.14","latest_agent_version":"0.2.15","update_available":` +
		boolText(update) + `,"agent_upgrade_allowed":` + boolText(allowed) + `,"agent_upgrade":` + last + `}}`
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func uaReply(host string, post func() (int, string)) func(*http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, host
		}
		return post()
	}
}

func TestUpgradeAgentConfirmFlow(t *testing.T) {
	cs, seen := vrSession(t, nil, uaReply(uaHostBody(true, true, ""), func() (int, string) {
		return http.StatusAccepted, `{"code":"ok","msg":"ok","payload":{"upgrade_id":"35749389-eb52-4e3a-9e82-2cf8807dbd8c","state":"running"}}`
	}))
	res, text, out := callVR(t, cs, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "up-1"})
	if res.IsError || !strings.Contains(text, "nothing ran yet") || !strings.Contains(text, "from 0.2.14 to 0.2.15") {
		t.Fatalf("plan %s", text)
	}
	id, _ := out["confirmation_id"].(string)
	if !confirmationIDPattern.MatchString(id) {
		t.Fatalf("confirmation id %q", id)
	}
	if calls := seen(); len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/hosts/"+uaHost {
		t.Fatalf("plan calls %#v", calls)
	}
	// A different key is refused and keeps the plan.
	res, text, _ = callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "other"})
	if !res.IsError || !strings.HasPrefix(text, "conflict:") || len(seen()) != 1 {
		t.Fatalf("key mismatch %s", text)
	}
	res, text, out = callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "up-1"})
	if res.IsError || out["state"] != "running" || out["to"] != "0.2.15" || !strings.Contains(text, "started") {
		t.Fatalf("confirm %s %#v", text, out)
	}
	calls := seen()
	if len(calls) != 2 || calls[1].Method != http.MethodPost || calls[1].Path != "/v1/hosts/"+uaHost+"/agent-upgrade" || calls[1].Body != `{"version":"0.2.15"}` {
		t.Fatalf("confirm calls %#v", calls)
	}
	// Used once: a second confirm is no longer this server's plan and goes
	// to the API's confirmations route (which answers not_found).
	_, _, _ = callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "up-1"})
	if len(seen()) != 3 || seen()[2].Path != "/v1/confirmations/"+id {
		t.Fatal("a used plan went to the host again")
	}
}

func TestUpgradeAgentRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		host, want string
	}{
		"not allowed": {uaHostBody(false, true, ""), "unsupported_agent: "},
		"up to date":  {uaHostBody(true, false, ""), "bad_request: "},
		"running":     {uaHostBody(true, true, "running"), "conflict: "},
	} {
		t.Run(name, func(t *testing.T) {
			cs, seen := vrSession(t, nil, uaReply(tc.host, func() (int, string) { return http.StatusInternalServerError, "" }))
			res, text, _ := callVR(t, cs, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"})
			if !res.IsError || !strings.HasPrefix(text, tc.want) || len(seen()) != 1 {
				t.Fatalf("%s: %s", name, text)
			}
		})
	}
	// An API refusal at confirm passes through.
	cs, _ := vrSession(t, nil, uaReply(uaHostBody(true, true, ""), func() (int, string) {
		return http.StatusServiceUnavailable, `{"code":"host_offline","msg":"pi3 is offline","payload":null}`
	}))
	_, _, out := callVR(t, cs, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"})
	res, text, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": out["confirmation_id"], "idempotency_key": "k"})
	if !res.IsError || text != "host_offline: pi3 is offline" {
		t.Fatalf("passthrough %s", text)
	}
	// Bad input never reaches the API.
	cs, seen := vrSession(t, nil, uaReply(uaHostBody(true, true, ""), nil))
	for _, args := range []map[string]any{{"host_id": "nope", "idempotency_key": "k"}, {"host_id": uaHost}} {
		if res, _, _ := callVR(t, cs, "upgrade_agent", args); !res.IsError {
			t.Fatalf("accepted %#v", args)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("bad input reached the API")
	}
}

func TestSummarizeHostsAgentUpgrade(t *testing.T) {
	got := summarizeHosts(json.RawMessage(`{"items":[
	 {"name":"pi1","status":"online","update_available":true,"latest_agent_version":"0.2.15","agent_upgrade_allowed":true,"agent_upgrade":null},
	 {"name":"pi2","status":"online","update_available":true,"latest_agent_version":"0.2.15","agent_upgrade_allowed":false,"agent_upgrade":{"to":"0.2.15","state":"rolled_back","reason":"not_reconnected"}}]}`), false)
	for _, want := range []string{"pi1 online (agent update available: 0.2.15; upgrade_agent can install it)", "upgrades from Deploybot are off", "agent upgrade to 0.2.15 rolled_back (not_reconnected)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary lacks %q: %s", want, got)
		}
	}
}

// Review L3: a plan belongs to the caller that asked for it, and one caller
// holds at most 10.
func TestUpgradeAgentCallerBinding(t *testing.T) {
	post := func() (int, string) {
		return http.StatusAccepted, `{"code":"ok","msg":"ok","payload":{"upgrade_id":"35749389-eb52-4e3a-9e82-2cf8807dbd8c","state":"running"}}`
	}
	alice, aliceSeen := vrSessionCaller(t, "", "alice-token", nil, uaReply(uaHostBody(true, true, ""), post))
	bob, bobSeen := vrSessionCaller(t, "", "bob-token", nil, uaReply(uaHostBody(true, true, ""), post))
	_, _, out := callVR(t, alice, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"})
	id, _ := out["confirmation_id"].(string)
	// Bob's confirm does not see Alice's plan: it goes to the API's route.
	callVR(t, bob, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "k"})
	if calls := bobSeen(); len(calls) != 1 || calls[0].Path != "/v1/confirmations/"+id {
		t.Fatalf("bob calls %#v", calls)
	}
	// Alice's plan is still there.
	res, text, _ := callVR(t, alice, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "k"})
	if res.IsError || !strings.Contains(text, "started") || aliceSeen()[1].Path != "/v1/hosts/"+uaHost+"/agent-upgrade" {
		t.Fatalf("alice confirm %s", text)
	}
	// At most 10 pending plans per caller; another caller is not affected.
	carol, _ := vrSessionCaller(t, "", "carol-token", nil, uaReply(uaHostBody(true, true, ""), post))
	for i := range 10 {
		if res, text, _ := callVR(t, carol, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"}); res.IsError {
			t.Fatalf("plan %d: %s", i, text)
		}
	}
	if res, text, _ := callVR(t, carol, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"}); !res.IsError || !strings.Contains(text, "too many") {
		t.Fatalf("11th plan %s", text)
	}
	if res, text, _ := callVR(t, bob, "upgrade_agent", map[string]any{"host_id": uaHost, "idempotency_key": "k"}); res.IsError {
		t.Fatalf("bob plan %s", text)
	}
}
