package tools

import (
	"context"
	"crypto/sha256"
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
	return vrSessionCaller(t, protocol, "test-caller", opts, reply)
}

// vrSessionCaller is vrSessionAt for one caller: every request carries the
// digest of caller as the caller key, as auth.RequireDelegated sets it.
func vrSessionCaller(t *testing.T, protocol, caller string, opts *mcp.ClientOptions, reply func(r *http.Request) (int, string)) (*mcp.ClientSession, func() []apiRequest) {
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
			ctx = auth.WithCallerKey(auth.WithDelegatedToken(ctx, "test-delegated", nil), sha256.Sum256([]byte(caller)))
			return next(ctx, method, req)
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
	return callVRResult(res)
}

// callVRResult splits a result into its text and structured content.
func callVRResult(res *mcp.CallToolResult) (*mcp.CallToolResult, string, map[string]any) {
	var structured map[string]any
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &structured)
	}
	return res, textOfResult(res), structured
}

func textOfResult(res *mcp.CallToolResult) string {
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	return text
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

// TestRequestEnvValueNoInputRequest: a 2026-07-28 client that declares elicitation.url
// gets the link as text in the first answer, with no URL input request, so
// a client that would wait for the person never blocks the call (DD 8hp8).
func TestRequestEnvValueNoInputRequest(t *testing.T) {
	var got *mcp.ElicitParams
	cs, seen := vrSessionAt(t, "2026-07-28", urlClient("accept", &got), createdReply)
	if v := cs.InitializeResult().ProtocolVersion; v != "2026-07-28" {
		t.Fatalf("protocol %s", v)
	}
	res, text, out := callVR(t, cs, "request_env_value", vrArgs)
	if res.IsError || got != nil {
		t.Fatalf("error %v elicited %#v: %s", res.IsError, got, text)
	}
	if !strings.HasPrefix(text, vrURL+"\n") || out["elicitation"] != elicitNotSupported || out["url"] != vrURL || len(out) != 4 {
		t.Fatalf("text %s structured %v", text, out)
	}
	if calls := seen(); len(calls) != 1 || calls[0].Method != http.MethodPost {
		t.Fatalf("calls %#v", calls)
	}
}

// TestRequestEnvValueLegacyRetry: a retry that carries the requestState and response of
// an input request an older gateway sent reads the state, creates nothing,
// and returns the link with the outcome.
func TestRequestEnvValueLegacyRetry(t *testing.T) {
	for action, want := range map[string]string{"accept": elicitAccepted, "decline": elicitDeclined, "cancel": elicitCancelled} {
		t.Run(action, func(t *testing.T) {
			cs, seen := vrSession(t, nil, createdReply)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:           "request_env_value",
				Arguments:      vrArgs,
				RequestState:   encodeRequestState(createdRequest{RequestID: vrID, URL: vrURL}),
				InputResponses: mcp.InputResponseMap{fillInputID: &mcp.ElicitResult{Action: action}},
			})
			if err != nil || res.IsError {
				t.Fatalf("%v %v", res, err)
			}
			_, text, out := callVRResult(res)
			calls := seen()
			if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/value-requests/"+vrID {
				t.Fatalf("calls %#v", calls)
			}
			if out["elicitation"] != want || out["state"] != "pending" || !strings.HasPrefix(text, vrURL+"\n") {
				t.Fatalf("structured %v text %s", out, text)
			}
		})
	}
}

// TestRequestEnvValueBadState: a forged requestState is refused and never
// reaches the API; a state for another key is refused after the read.
func TestRequestEnvValueBadState(t *testing.T) {
	cs, seen := vrSession(t, nil, createdReply)
	for _, state := range []string{"not-base64!", encodeRequestState(createdRequest{RequestID: vrID, URL: "https://evil.example/x"})} {
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
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_env_value", Arguments: other, RequestState: encodeRequestState(createdRequest{RequestID: vrID, URL: vrURL})})
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
	created := createdRequest{RequestID: vrID, URL: vrURL, ExpiresAt: "2026-11-15T10:15:00Z"}
	for state, want := range map[string]string{
		"pending":  "is pending; it can be filled until 2026-11-15T10:15:00Z.",
		"filled":   "is filled: the value is set, and you will not see it.",
		"declined": "was declined by the person.",
		"expired":  "has expired without a value.",
	} {
		text := valueRequestKind.summary(created, "services.K", state, elicitDeclined)
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
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_env_value", Arguments: vrArgs, RequestState: encodeRequestState(createdRequest{RequestID: vrID, URL: vrURL})})
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
