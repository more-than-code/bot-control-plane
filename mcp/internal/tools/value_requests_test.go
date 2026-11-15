package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
	"github.com/more-than-code/bot-control-plane/mcp/internal/auth"
)

const (
	vrID      = "Abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE"
	vrURL     = "https://console.example/fill/" + vrID
	vrProject = "11111111-1111-4111-8111-111111111111"
)

// vrSession is an in-memory session against a fake API. opts sets the
// client's capabilities and elicitation handler.
func vrSession(t *testing.T, opts *mcp.ClientOptions, reply func(r *http.Request) (int, string)) (*mcp.ClientSession, func() []apiRequest) {
	t.Helper()
	return vrSessionAt(t, "", opts, reply)
}

// vrSessionAt is vrSession on one protocol version ("" is the SDK's latest).
func vrSessionAt(t *testing.T, protocol string, opts *mcp.ClientOptions, reply func(r *http.Request) (int, string)) (*mcp.ClientSession, func() []apiRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []apiRequest
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, apiRequest{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		mu.Unlock()
		status, payload := reply(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(fake.Close)
	api, err := apiclient.New(fake.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(server, api)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(auth.WithDelegatedToken(ctx, "test-delegated", nil), method, req)
		}
	})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, opts).Connect(ctx, clientT, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, func() []apiRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]apiRequest{}, seen...)
	}
}

func createdReply(r *http.Request) (int, string) {
	if r.Method == http.MethodPost {
		return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + vrID + `","url":"` + vrURL + `","expires_at":"2026-11-15T10:15:00Z"}}`
	}
	return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + vrID + `","project_id":"` + vrProject + `","set":"services","key":"STRIPE_API_KEY","state":"pending","expires_at":"2026-11-15T10:15:00Z","created_at":"2026-11-15T10:00:00Z"}}`
}

func callVR(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	var structured map[string]any
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &structured)
	}
	return res, text, structured
}

var vrArgs = map[string]any{"project_id": vrProject, "set": "services", "key": "STRIPE_API_KEY", "reason": "Checkout needs it"}

// TestRequestEnvValueLink: a client without elicitation.url gets the link as text.
func TestRequestEnvValueLink(t *testing.T) {
	cs, seen := vrSession(t, nil, createdReply)
	res, text, out := callVR(t, cs, "request_env_value", vrArgs)
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/projects/"+vrProject+"/env-sets/services/value-requests" {
		t.Fatalf("calls %#v", calls)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil || body["key"] != "STRIPE_API_KEY" || body["reason"] != "Checkout needs it" || len(body) != 2 {
		t.Fatalf("body %s", calls[0].Body)
	}
	for _, want := range []string{valueRule, vrURL, "Show the person this link", "get_env_value_request"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q: %s", want, text)
		}
	}
	if out["elicitation"] != elicitNotSupported || out["url"] != vrURL || out["request_id"] != vrID || len(out) != 4 {
		t.Fatalf("structured %v", out)
	}
}

func urlClient(action string, got **mcp.ElicitParams) *mcp.ClientOptions {
	return &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			*got = req.Params
			return &mcp.ElicitResult{Action: action}, nil
		},
	}
}

// TestRequestEnvValueOldProtocol: a 2025-11-25 client gets the link as
// text even when it declares elicitation.url (spec rev 2 §3.1 step 3).
func TestRequestEnvValueOldProtocol(t *testing.T) {
	var got *mcp.ElicitParams
	cs, seen := vrSessionAt(t, "2025-11-25", urlClient("accept", &got), createdReply)
	if v := cs.InitializeResult().ProtocolVersion; v != "2025-11-25" {
		t.Fatalf("protocol %s", v)
	}
	_, text, out := callVR(t, cs, "request_env_value", vrArgs)
	if got != nil || out["elicitation"] != elicitNotSupported || !strings.Contains(text, vrURL) {
		t.Fatalf("elicited %v %v %s", got, out["elicitation"], text)
	}
	if n := len(seen()); n != 1 {
		t.Fatalf("api calls %d", n)
	}
}

// TestRequestEnvValueInputRequest: a 2026-07-28 client that declares
// elicitation.url gets a URL-mode input request (elicitationId = request
// id); its retry reads the state, creates nothing, and returns the link.
func TestRequestEnvValueInputRequest(t *testing.T) {
	for action, want := range map[string]string{"accept": elicitAccepted, "decline": elicitDeclined, "cancel": elicitCancelled} {
		t.Run(action, func(t *testing.T) {
			var got *mcp.ElicitParams
			cs, seen := vrSessionAt(t, newProtocol, urlClient(action, &got), createdReply)
			if v := cs.InitializeResult().ProtocolVersion; v != newProtocol {
				t.Fatalf("protocol %s", v)
			}
			res, text, out := callVR(t, cs, "request_env_value", vrArgs)
			if res.IsError {
				t.Fatalf("error %s", text)
			}
			if got == nil || got.Mode != "url" || got.URL != vrURL || got.ElicitationID != vrID || got.RequestedSchema != nil {
				t.Fatalf("elicit params %#v", got)
			}
			if !strings.Contains(got.Message, "services.STRIPE_API_KEY") || !strings.Contains(got.Message, "never sees the value") {
				t.Fatalf("message %q", got.Message)
			}
			calls := seen()
			if len(calls) != 2 || calls[0].Method != http.MethodPost || calls[1].Method != http.MethodGet || calls[1].Path != "/v1/value-requests/"+vrID {
				t.Fatalf("calls %#v", calls)
			}
			if out["elicitation"] != want || out["state"] != "pending" || out["url"] != vrURL {
				t.Fatalf("structured %v", out)
			}
			if !strings.Contains(text, vrURL) || !strings.Contains(text, valueRule) || !strings.Contains(text, "is pending; it can be filled until 2026-11-15T10:15:00Z") {
				t.Fatalf("text %s", text)
			}
			if action != "accept" && !strings.Contains(text, "stays open until it expires") {
				t.Fatalf("text %s", text)
			}
		})
	}
}

// TestRequestEnvValueBadState: a forged requestState is refused and never
// reaches the API; a state for another key is refused after the read.
func TestRequestEnvValueBadState(t *testing.T) {
	cs, seen := vrSession(t, nil, createdReply)
	for _, state := range []string{"not-base64!", encodeVRState(createdRequest{RequestID: vrID, URL: "https://evil.example/x"})} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_env_value", Arguments: vrArgs, RequestState: state})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("state %q accepted", state)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a forged state reached the API")
	}
	other := map[string]any{"project_id": vrProject, "set": "services", "key": "OTHER_KEY"}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_env_value", Arguments: other, RequestState: encodeVRState(createdRequest{RequestID: vrID, URL: vrURL})})
	if err != nil || !res.IsError {
		t.Fatalf("other key %v %v", res, err)
	}
	for _, c := range seen() {
		if c.Method == http.MethodPost {
			t.Fatal("a retry created a request")
		}
	}
}

// TestRequestEnvValueFormOnlyClient: elicitation without the url mode is not used.
func TestRequestEnvValueFormOnlyClient(t *testing.T) {
	called := false
	opts := &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		called = true
		return &mcp.ElicitResult{Action: "accept"}, nil
	}}
	cs, _ := vrSession(t, opts, createdReply)
	_, _, out := callVR(t, cs, "request_env_value", vrArgs)
	if called || out["elicitation"] != elicitNotSupported {
		t.Fatalf("form-only client elicited %v %v", called, out["elicitation"])
	}
}

func TestRequestEnvValueValidation(t *testing.T) {
	cs, seen := vrSession(t, nil, createdReply)
	for name, args := range map[string]map[string]any{
		"project":  {"project_id": "nope", "set": "services", "key": "K"},
		"set":      {"project_id": vrProject, "set": "Bad Set", "key": "K"},
		"key":      {"project_id": vrProject, "set": "services", "key": "lower"},
		"reason":   {"project_id": vrProject, "set": "services", "key": "K", "reason": strings.Repeat("a", 201)},
		"newline":  {"project_id": vrProject, "set": "services", "key": "K", "reason": "a\nb"},
		"bidi":     {"project_id": vrProject, "set": "services", "key": "K", "reason": "pay \u202eevil"},
		"no value": {"project_id": vrProject, "set": "services", "key": "K", "value": "secret"},
	} {
		res, text, _ := callVR(t, cs, "request_env_value", args)
		if !res.IsError || !strings.HasPrefix(text, "bad_request") {
			t.Fatalf("%s: %v %s", name, res.IsError, text)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a bad call reached the API")
	}
}

// TestRequestEnvValueBadURL: a url that is not the console fill page is not shown.
func TestRequestEnvValueBadURL(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "https://console.example/fill/other", "https://console.example/fill/" + vrID + "?x=1", "https://u:p@console.example/fill/" + vrID} {
		cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
			return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + vrID + `","url":"` + bad + `","expires_at":"x"}}`
		})
		res, text, _ := callVR(t, cs, "request_env_value", vrArgs)
		if !res.IsError || strings.Contains(text, bad) {
			t.Fatalf("%s: %s", bad, text)
		}
	}
}

func TestRequestEnvValueAPIError(t *testing.T) {
	cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusConflict, `{"code":"conflict","msg":"STRIPE_API_KEY already has a value; edit it in the console","payload":null}`
	})
	res, text, _ := callVR(t, cs, "request_env_value", vrArgs)
	if !res.IsError || text != "conflict: STRIPE_API_KEY already has a value; edit it in the console" {
		t.Fatalf("%v %s", res.IsError, text)
	}
}

func TestGetEnvValueRequest(t *testing.T) {
	cs, seen := vrSession(t, nil, createdReply)
	res, text, out := callVR(t, cs, "get_env_value_request", map[string]any{"request_id": vrID})
	if res.IsError || text != "services.STRIPE_API_KEY: pending. The person has not entered the value yet." {
		t.Fatalf("%v %s", res.IsError, text)
	}
	if calls := seen(); len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/value-requests/"+vrID {
		t.Fatalf("calls %#v", calls)
	}
	if out["state"] != "pending" || len(out) != 7 {
		t.Fatalf("structured %v", out)
	}
	res, text, _ = callVR(t, cs, "get_env_value_request", map[string]any{"request_id": "../x"})
	if !res.IsError || !strings.HasPrefix(text, "bad_request") {
		t.Fatalf("bad id %s", text)
	}
	// A body with a value-like extra field is not passed on.
	cs, _ = vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + vrID + `","project_id":"` + vrProject + `","set":"services","key":"K","state":"pending","expires_at":"e","created_at":"c","value":"leak"}}`
	})
	_, text, out = callVR(t, cs, "get_env_value_request", map[string]any{"request_id": vrID})
	if _, ok := out["value"]; ok || strings.Contains(text, "leak") {
		t.Fatalf("value passed on: %v %s", out, text)
	}
}

// TestRequestSummaryPerState: the retry text reads right for every state (review item 10).
func TestRequestSummaryPerState(t *testing.T) {
	in := requestEnvValueArgs{Set: "services", Key: "K"}
	created := createdRequest{RequestID: vrID, URL: vrURL, ExpiresAt: "2026-11-15T10:15:00Z"}
	for state, want := range map[string]string{
		"pending":  "is pending; it can be filled until 2026-11-15T10:15:00Z.",
		"filled":   "is filled: the value is set, and you will not see it.",
		"declined": "was declined by the person.",
		"expired":  "has expired without a value.",
	} {
		text := requestSummary(in, created, state, elicitDeclined)
		if !strings.Contains(text, want) || !strings.Contains(text, vrURL) || strings.Contains(text, "(open until") {
			t.Errorf("%s: %s", state, text)
		}
		if (state == "pending") != strings.Contains(text, "stays open until it expires") {
			t.Errorf("%s: open sentence %s", state, text)
		}
	}
}

// TestGetEnvValueRequestNotFound: an API 404 is a tool error with the API text.
func TestGetEnvValueRequestNotFound(t *testing.T) {
	cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
	})
	res, text, _ := callVR(t, cs, "get_env_value_request", map[string]any{"request_id": vrID})
	if !res.IsError || text != "not_found: not found" {
		t.Fatalf("%v %s", res.IsError, text)
	}
}

// TestRequestEnvValueRetryReadFails: a retry whose state read fails is a
// tool error and never creates a request.
func TestRequestEnvValueRetryReadFails(t *testing.T) {
	cs, seen := vrSession(t, nil, func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
		}
		return createdReply(r)
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_env_value", Arguments: vrArgs, RequestState: encodeVRState(createdRequest{RequestID: vrID, URL: vrURL})})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a failed read was not an error")
	}
	calls := seen()
	if len(calls) != 1 || calls[0].Method != http.MethodGet {
		t.Fatalf("calls %#v", calls)
	}
}

// TestRequestEnvValueConflictPlainKey: a key the redaction leaves alone
// keeps the API message unchanged.
func TestRequestEnvValueConflictPlainKey(t *testing.T) {
	cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusConflict, `{"code":"conflict","msg":"PORT already has a value; edit it in the console","payload":null}`
	})
	args := map[string]any{"project_id": vrProject, "set": "services", "key": "PORT"}
	res, text, _ := callVR(t, cs, "request_env_value", args)
	if !res.IsError || text != "conflict: PORT already has a value; edit it in the console" {
		t.Fatalf("%v %s", res.IsError, text)
	}
}
