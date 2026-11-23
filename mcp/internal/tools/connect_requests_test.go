package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	crID    = "Zyxwvutsrqponmlkjihgfedcba9876543210_-ABCDE"
	crURL   = "https://console.example/connect/" + crID
	crToken = "gho_PLANTEDtokenVALUE0000"
)

var crArgs = map[string]any{"project_id": vrProject, "registry": "ghcr.io", "reason": "Private image"}

// crReply is the fake API: create, then the read. Both carry a planted
// token-shaped field that must never reach the agent.
func crReply(state string) func(r *http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + crID + `","url":"` + crURL + `","expires_at":"2026-11-16T10:15:00Z","token":"` + crToken + `"}}`
		}
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + crID + `","project_id":"` + vrProject + `","registry":"ghcr.io","state":"` + state + `","expires_at":"2026-11-16T10:15:00Z","created_at":"2026-11-16T10:00:00Z","token":"` + crToken + `","reason":"r"}}`
	}
}

func assertNoToken(t *testing.T, text string, out map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(out)
	if strings.Contains(text, crToken) || strings.Contains(string(raw), crToken) {
		t.Fatalf("token passed on: %s %s", text, raw)
	}
}

// TestConnectRegistryLink: a client without elicitation.url gets the link as text.
func TestConnectRegistryLink(t *testing.T) {
	cs, seen := vrSession(t, nil, crReply("pending"))
	res, text, out := callVR(t, cs, "connect_registry", crArgs)
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/projects/"+vrProject+"/registries/github/requests" || calls[0].Body != `{"reason":"Private image"}` {
		t.Fatalf("calls %#v", calls)
	}
	for _, want := range []string{connectRule, crURL, "Show the person this link", "github.com", "get_registry_connect_request"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q: %s", want, text)
		}
	}
	if out["elicitation"] != elicitNotSupported || out["url"] != crURL || out["request_id"] != crID || len(out) != 4 {
		t.Fatalf("structured %v", out)
	}
	assertNoToken(t, text, out)
	// Without a reason the body is an empty object.
	cs, seen = vrSession(t, nil, crReply("pending"))
	callVR(t, cs, "connect_registry", map[string]any{"project_id": vrProject, "registry": "ghcr.io"})
	if calls := seen(); calls[0].Body != `{}` {
		t.Fatalf("body %s", calls[0].Body)
	}
}

// TestConnectRegistryOldProtocol: a 2025-11-25 client gets text only.
func TestConnectRegistryOldProtocol(t *testing.T) {
	var got *mcp.ElicitParams
	cs, seen := vrSessionAt(t, "2025-11-25", urlClient("accept", &got), crReply("pending"))
	_, text, out := callVR(t, cs, "connect_registry", crArgs)
	if got != nil || out["elicitation"] != elicitNotSupported || !strings.Contains(text, crURL) || len(seen()) != 1 {
		t.Fatalf("elicited %v %v %s", got, out["elicitation"], text)
	}
}

// TestConnectRegistryNoInputRequest: a 2026-07-28 client that declares elicitation.url
// gets the link as text in the first answer, with no URL input request, so
// a client that would wait for the person never blocks the call (DD 8hp8).
func TestConnectRegistryNoInputRequest(t *testing.T) {
	var got *mcp.ElicitParams
	cs, seen := vrSessionAt(t, "2026-07-28", urlClient("accept", &got), crReply("pending"))
	if v := cs.InitializeResult().ProtocolVersion; v != "2026-07-28" {
		t.Fatalf("protocol %s", v)
	}
	res, text, out := callVR(t, cs, "connect_registry", crArgs)
	if res.IsError || got != nil {
		t.Fatalf("error %v elicited %#v: %s", res.IsError, got, text)
	}
	if !strings.HasPrefix(text, crURL+"\n") || out["elicitation"] != elicitNotSupported || out["url"] != crURL || len(out) != 4 {
		t.Fatalf("text %s structured %v", text, out)
	}
	if calls := seen(); len(calls) != 1 || calls[0].Method != http.MethodPost {
		t.Fatalf("calls %#v", calls)
	}
	assertNoToken(t, text, out)
}

// TestConnectRegistryLegacyRetry: a retry that carries the requestState and response of
// an input request an older gateway sent reads the state, creates nothing,
// and returns the link with the outcome.
func TestConnectRegistryLegacyRetry(t *testing.T) {
	for action, want := range map[string]string{"accept": elicitAccepted, "decline": elicitDeclined, "cancel": elicitCancelled} {
		t.Run(action, func(t *testing.T) {
			cs, seen := vrSession(t, nil, crReply("pending"))
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:           "connect_registry",
				Arguments:      crArgs,
				RequestState:   encodeRequestState(createdRequest{RequestID: crID, URL: crURL}),
				InputResponses: mcp.InputResponseMap{fillInputID: &mcp.ElicitResult{Action: action}},
			})
			if err != nil || res.IsError {
				t.Fatalf("%v %v", res, err)
			}
			_, text, out := callVRResult(res)
			calls := seen()
			if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/registry-connect-requests/"+crID {
				t.Fatalf("calls %#v", calls)
			}
			if out["elicitation"] != want || out["state"] != "pending" || !strings.HasPrefix(text, crURL+"\n") {
				t.Fatalf("structured %v text %s", out, text)
			}
		})
	}
}

// TestConnectRegistryBadState: a forged state never reaches the API; a
// state of another project is refused after the read.
func TestConnectRegistryBadState(t *testing.T) {
	cs, seen := vrSession(t, nil, crReply("pending"))
	for _, state := range []string{"not-base64!", encodeRequestState(createdRequest{RequestID: crID, URL: "https://console.example/fill/" + crID})} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "connect_registry", Arguments: crArgs, RequestState: state})
		if err != nil || !res.IsError {
			t.Fatalf("state %q accepted %v", state, err)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a forged state reached the API")
	}
	other := map[string]any{"project_id": "22222222-2222-4222-8222-222222222222", "registry": "ghcr.io"}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "connect_registry", Arguments: other, RequestState: encodeRequestState(createdRequest{RequestID: crID, URL: crURL})})
	if err != nil || !res.IsError {
		t.Fatalf("other project %v %v", res, err)
	}
	for _, c := range seen() {
		if c.Method == http.MethodPost {
			t.Fatal("a retry created a request")
		}
	}
}

func TestConnectRegistryValidation(t *testing.T) {
	cs, seen := vrSession(t, nil, crReply("pending"))
	for name, args := range map[string]map[string]any{
		"project":  {"project_id": "nope", "registry": "ghcr.io"},
		"registry": {"project_id": vrProject, "registry": "docker.io"},
		"missing":  {"project_id": vrProject},
		"reason":   {"project_id": vrProject, "registry": "ghcr.io", "reason": strings.Repeat("a", 201)},
		"bidi":     {"project_id": vrProject, "registry": "ghcr.io", "reason": "x‮y"},
		"token":    {"project_id": vrProject, "registry": "ghcr.io", "token": "gho_x"},
	} {
		res, text, _ := callVR(t, cs, "connect_registry", args)
		if !res.IsError || !strings.HasPrefix(text, "bad_request") {
			t.Fatalf("%s: %v %s", name, res.IsError, text)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a bad call reached the API")
	}
}

// TestConnectRegistryBadURL: a url that is not the console connect page is not shown.
func TestConnectRegistryBadURL(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "https://console.example/fill/" + crID, "https://console.example/connect/" + crID + "?x=1", "https://u:p@console.example/connect/" + crID} {
		cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
			return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + crID + `","url":"` + bad + `","expires_at":"x"}}`
		})
		res, text, _ := callVR(t, cs, "connect_registry", crArgs)
		if !res.IsError || strings.Contains(text, bad) {
			t.Fatalf("%s: %s", bad, text)
		}
	}
}

func TestConnectRegistryAPIError(t *testing.T) {
	cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusServiceUnavailable, `{"code":"unavailable","msg":"GitHub is not configured","payload":null}`
	})
	res, text, _ := callVR(t, cs, "connect_registry", crArgs)
	if !res.IsError || text != "unavailable: GitHub is not configured" {
		t.Fatalf("%v %s", res.IsError, text)
	}
}

func TestGetRegistryConnectRequest(t *testing.T) {
	if connectEndedHint != "If the person finished on GitHub anyway, check_readiness shows whether ghcr.io is now connected." {
		t.Fatal("hint text")
	}
	for state, want := range map[string]string{
		"pending":   "ghcr.io: pending. The person has not connected GitHub yet.",
		"connected": "ghcr.io: connected. The project pulls ghcr.io images with the GitHub login; you will not see the token.",
		"declined":  "ghcr.io: declined by the person.\n" + connectEndedHint,
		"expired":   "ghcr.io: expired without a connection. Ask again with connect_registry if it is still needed.\n" + connectEndedHint,
	} {
		cs, seen := vrSession(t, nil, crReply(state))
		res, text, out := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": crID})
		if res.IsError || text != want {
			t.Fatalf("%s: %v %s", state, res.IsError, text)
		}
		if calls := seen(); len(calls) != 1 || calls[0].Path != "/v1/registry-connect-requests/"+crID {
			t.Fatalf("calls %#v", calls)
		}
		if out["state"] != state || out["registry"] != "ghcr.io" || out["last_failure"] != nil || len(out) != 7 {
			t.Fatalf("structured %v", out)
		}
		if _, ok := out["reason"]; ok {
			t.Fatal("reason passed on")
		}
		assertNoToken(t, text, out)
	}
	cs, _ := vrSession(t, nil, crReply("pending"))
	res, text, _ := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": "../x"})
	if !res.IsError || !strings.HasPrefix(text, "bad_request") {
		t.Fatalf("bad id %s", text)
	}
	cs, _ = vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + crID + `","project_id":"` + vrProject + `","registry":"docker.io","state":"pending"}}`
	})
	if res, _, _ := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": crID}); !res.IsError {
		t.Fatal("a wrong registry was accepted")
	}
}

// TestGetRegistryConnectRequestLastFailure: api-v1 v1.39 item 2 in plain
// words, and an unknown reason is refused as a shape error.
func TestGetRegistryConnectRequestLastFailure(t *testing.T) {
	reply := func(state, reason string) func(*http.Request) (int, string) {
		return func(*http.Request) (int, string) {
			return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + crID + `","project_id":"` + vrProject + `","registry":"ghcr.io","state":"` + state + `","expires_at":"2026-11-16T10:15:00Z","created_at":"2026-11-16T10:00:00Z","last_failure":{"reason":"` + reason + `","at":"2026-11-16T10:05:00Z"}}}`
		}
	}
	for reason, words := range map[string]string{
		"expired_sign_in": "the GitHub sign-in expired or was already used",
		"scope_missing":   "GitHub did not grant read:packages",
		"github_error":    "GitHub sign-in failed",
		"forbidden":       "the person is no longer owner or admin of the project",
		"save_failed":     "Deploybot could not save the GitHub login",
	} {
		cs, _ := vrSession(t, nil, reply("pending", reason))
		res, text, out := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": crID})
		want := "ghcr.io: pending. The last attempt failed: " + words + " (at 2026-11-16T10:05:00Z). The request is still open: tell the person why and ask them to open the link again and start over."
		if res.IsError || text != want {
			t.Fatalf("%s: %s", reason, text)
		}
		lf, _ := out["last_failure"].(map[string]any)
		if lf["reason"] != reason || lf["at"] != "2026-11-16T10:05:00Z" || len(lf) != 2 {
			t.Fatalf("%s: structured %v", reason, out)
		}
	}
	cs, _ := vrSession(t, nil, reply("expired", "scope_missing"))
	if _, text, _ := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": crID}); !strings.HasPrefix(text, "ghcr.io: expired without a connection. The last attempt failed: GitHub did not grant read:packages") {
		t.Fatalf("expired: %s", text)
	}
	cs, _ = vrSession(t, nil, reply("pending", "other"))
	if res, _, _ := callVR(t, cs, "get_registry_connect_request", map[string]any{"request_id": crID}); !res.IsError {
		t.Fatal("an unknown reason was accepted")
	}
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == "get_registry_connect_request" {
			found = strings.Contains(tool.Description, "last_failure") && strings.Contains(tool.Description, "ask them to open the link again and start over")
		}
	}
	if !found {
		t.Fatal("the description does not say what to do after a failure")
	}
}

// TestConnectSummaryPerState: the retry text reads right for every state.
func TestConnectSummaryPerState(t *testing.T) {
	created := createdRequest{RequestID: crID, URL: crURL, ExpiresAt: "2026-11-16T10:15:00Z"}
	for state, want := range map[string]string{
		"pending":   "is pending; it can be used until 2026-11-16T10:15:00Z.",
		"connected": "is connected",
		"declined":  "was declined by the person.",
		"expired":   "has expired without a connection.",
	} {
		text := connectSummary(created, state, elicitDeclined)
		if !strings.Contains(text, want) || !strings.Contains(text, crURL) {
			t.Errorf("%s: %s", state, text)
		}
		if (state == "pending") != strings.Contains(text, "stays open until it expires") {
			t.Errorf("%s: open sentence %s", state, text)
		}
	}
}
