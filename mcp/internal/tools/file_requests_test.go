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
	frID   = "Fbcdefghijklmnopqrstuvwxyz0123456789_-ABCDE"
	frURL  = "https://console.example/fill-file/" + frID
	frLeak = "PEM-CONTENT-must-not-appear"
)

func fileCreatedReply(r *http.Request) (int, string) {
	if r.Method == http.MethodPost {
		return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + frID + `","url":"` + frURL + `","expires_at":"2026-11-16T10:15:00Z"}}`
	}
	return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"request_id":"` + frID + `","project_id":"` + vrProject + `","name":"tls.pem","secret":true,"state":"pending","expires_at":"2026-11-16T10:15:00Z","created_at":"2026-11-16T10:00:00Z","content":"` + frLeak + `"}}`
}

var frArgs = map[string]any{"project_id": vrProject, "name": "tls.pem", "reason": "TLS key for the proxy"}

// TestRequestFileLink: a client without elicitation.url gets the link as text.
func TestRequestFileLink(t *testing.T) {
	cs, seen := vrSession(t, nil, fileCreatedReply)
	res, text, out := callVR(t, cs, "request_file", frArgs)
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	calls := seen()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/projects/"+vrProject+"/file-requests" {
		t.Fatalf("calls %#v", calls)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil || body["name"] != "tls.pem" || body["reason"] != "TLS key for the proxy" || len(body) != 2 {
		t.Fatalf("body %s", calls[0].Body)
	}
	for _, want := range []string{fileRule, frURL, "Show the person this link", "get_file_request"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q: %s", want, text)
		}
	}
	if out["elicitation"] != elicitNotSupported || out["url"] != frURL || out["request_id"] != frID || len(out) != 4 {
		t.Fatalf("structured %v", out)
	}
	// secret false is passed on.
	_, _, _ = callVR(t, cs, "request_file", map[string]any{"project_id": vrProject, "name": "app.conf", "secret": false})
	calls = seen()
	if err := json.Unmarshal([]byte(calls[len(calls)-1].Body), &body); err != nil || body["secret"] != false {
		t.Fatalf("secret body %s", calls[len(calls)-1].Body)
	}
}

// TestRequestFileInputRequest: a 2026-07-28 client that declares
// elicitation.url gets a URL-mode input request; its retry reads the
// state, creates nothing, and returns the link. No content in any result.
func TestRequestFileInputRequest(t *testing.T) {
	for action, want := range map[string]string{"accept": elicitAccepted, "decline": elicitDeclined, "cancel": elicitCancelled} {
		t.Run(action, func(t *testing.T) {
			var got *mcp.ElicitParams
			cs, seen := vrSessionAt(t, newProtocol, urlClient(action, &got), fileCreatedReply)
			res, text, out := callVR(t, cs, "request_file", frArgs)
			if res.IsError {
				t.Fatalf("error %s", text)
			}
			if got == nil || got.Mode != "url" || got.URL != frURL || got.ElicitationID != frID || got.RequestedSchema != nil {
				t.Fatalf("elicit params %#v", got)
			}
			if !strings.Contains(got.Message, "tls.pem") || !strings.Contains(got.Message, "never sees the content") {
				t.Fatalf("message %q", got.Message)
			}
			calls := seen()
			if len(calls) != 2 || calls[0].Method != http.MethodPost || calls[1].Method != http.MethodGet || calls[1].Path != "/v1/file-requests/"+frID {
				t.Fatalf("calls %#v", calls)
			}
			if out["elicitation"] != want || out["state"] != "pending" || out["url"] != frURL {
				t.Fatalf("structured %v", out)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(text, frLeak) || strings.Contains(string(raw), frLeak) {
				t.Fatal("content passed on")
			}
			if !strings.Contains(text, frURL) || !strings.Contains(text, fileRule) || !strings.Contains(text, "is pending; it can be filled until 2026-11-16T10:15:00Z") {
				t.Fatalf("text %s", text)
			}
		})
	}
}

// TestRequestFileOldProtocol: a 2025-11-25 client gets the link only.
func TestRequestFileOldProtocol(t *testing.T) {
	var got *mcp.ElicitParams
	cs, seen := vrSessionAt(t, "2025-11-25", urlClient("accept", &got), fileCreatedReply)
	_, text, out := callVR(t, cs, "request_file", frArgs)
	if got != nil || out["elicitation"] != elicitNotSupported || !strings.Contains(text, frURL) || len(seen()) != 1 {
		t.Fatalf("elicited %v %v %s", got, out["elicitation"], text)
	}
}

// TestRequestFileBadState: a forged requestState, or a value-request state,
// is refused before the API; a state for another file is refused after the read.
func TestRequestFileBadState(t *testing.T) {
	cs, seen := vrSession(t, nil, fileCreatedReply)
	for _, state := range []string{
		"not-base64!",
		encodeRequestState(createdRequest{RequestID: frID, URL: "https://evil.example/x"}),
		encodeRequestState(createdRequest{RequestID: vrID, URL: vrURL}),
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_file", Arguments: frArgs, RequestState: state})
		if err != nil || !res.IsError {
			t.Fatalf("state %q accepted", state)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a forged state reached the API")
	}
	other := map[string]any{"project_id": vrProject, "name": "other.pem"}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_file", Arguments: other, RequestState: encodeRequestState(createdRequest{RequestID: frID, URL: frURL})})
	if err != nil || !res.IsError {
		t.Fatalf("other file %v %v", res, err)
	}
	for _, c := range seen() {
		if c.Method == http.MethodPost {
			t.Fatal("a retry created a request")
		}
	}
}

func TestRequestFileValidation(t *testing.T) {
	cs, seen := vrSession(t, nil, fileCreatedReply)
	for name, args := range map[string]map[string]any{
		"project":    {"project_id": "nope", "name": "a.txt"},
		"upper":      {"project_id": vrProject, "name": "A.txt"},
		"dot":        {"project_id": vrProject, "name": ".."},
		"long":       {"project_id": vrProject, "name": strings.Repeat("a", 65)},
		"reason":     {"project_id": vrProject, "name": "a.txt", "reason": strings.Repeat("a", 201)},
		"newline":    {"project_id": vrProject, "name": "a.txt", "reason": "a\nb"},
		"bidi":       {"project_id": vrProject, "name": "a.txt", "reason": "pay ‮evil"},
		"no content": {"project_id": vrProject, "name": "a.txt", "content": "secret"},
	} {
		res, text, _ := callVR(t, cs, "request_file", args)
		if !res.IsError || !strings.HasPrefix(text, "bad_request") {
			t.Fatalf("%s: %v %s", name, res.IsError, text)
		}
	}
	if len(seen()) != 0 {
		t.Fatal("a bad call reached the API")
	}
}

// TestRequestFileBadURL: a url that is not the console file page is not shown.
func TestRequestFileBadURL(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "https://console.example/fill/" + frID, "https://console.example/fill-file/" + frID + "?x=1", "https://u:p@console.example/fill-file/" + frID} {
		cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
			return http.StatusCreated, `{"code":"ok","msg":"ok","payload":{"request_id":"` + frID + `","url":"` + bad + `","expires_at":"x"}}`
		})
		res, text, _ := callVR(t, cs, "request_file", frArgs)
		if !res.IsError || strings.Contains(text, bad) {
			t.Fatalf("%s: %s", bad, text)
		}
	}
}

// TestRequestFileExistsKeepsName: the file-exists conflict keeps its fixed
// text even for names the generic redaction would read as a secret.
func TestRequestFileExistsKeepsName(t *testing.T) {
	for _, name := range []string{"credentials.json", "secrets.env", "api_key.txt", "password.conf", "tls.pem"} {
		msg := "file " + name + " already exists; replace it in the console"
		cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
			return http.StatusConflict, `{"code":"conflict","msg":"` + msg + `","payload":null}`
		})
		res, text, _ := callVR(t, cs, "request_file", map[string]any{"project_id": vrProject, "name": name})
		if !res.IsError || text != "conflict: "+msg {
			t.Fatalf("%s: %v %s", name, res.IsError, text)
		}
	}
	// Another conflict text is still redacted as before.
	other := "token=" + frLeak
	cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusConflict, `{"code":"conflict","msg":"` + other + `","payload":null}`
	})
	_, text, _ := callVR(t, cs, "request_file", frArgs)
	if strings.Contains(text, frLeak) {
		t.Fatalf("redaction skipped: %s", text)
	}
}

// TestRequestFileAPIErrors: the contract texts reach the agent unchanged.
func TestRequestFileAPIErrors(t *testing.T) {
	for _, c := range []struct{ status, code, msg string }{
		{"409", "conflict", "file tls.pem already exists; replace it in the console"},
		{"409", "conflict", "too many open file requests; fill or wait for the ones you have"},
		{"400", "bad_request", "too many files"},
	} {
		cs, _ := vrSession(t, nil, func(*http.Request) (int, string) {
			status := http.StatusConflict
			if c.status == "400" {
				status = http.StatusBadRequest
			}
			return status, `{"code":"` + c.code + `","msg":"` + c.msg + `","payload":null}`
		})
		res, text, _ := callVR(t, cs, "request_file", frArgs)
		if !res.IsError || text != c.code+": "+c.msg {
			t.Fatalf("%v %s", res.IsError, text)
		}
	}
}

func TestGetFileRequest(t *testing.T) {
	cs, seen := vrSession(t, nil, fileCreatedReply)
	res, text, out := callVR(t, cs, "get_file_request", map[string]any{"request_id": frID})
	if res.IsError || text != "tls.pem: pending. The person has not added the file yet." {
		t.Fatalf("%v %s", res.IsError, text)
	}
	if calls := seen(); len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/file-requests/"+frID {
		t.Fatalf("calls %#v", calls)
	}
	raw, _ := json.Marshal(out)
	if out["state"] != "pending" || out["secret"] != true || len(out) != 7 || strings.Contains(string(raw), frLeak) {
		t.Fatalf("structured %v", out)
	}
	res, text, _ = callVR(t, cs, "get_file_request", map[string]any{"request_id": "../x"})
	if !res.IsError || !strings.HasPrefix(text, "bad_request") {
		t.Fatalf("bad id %s", text)
	}
}

// TestFileRequestSummaryPerState: the retry text reads right for every state.
func TestFileRequestSummaryPerState(t *testing.T) {
	created := createdRequest{RequestID: frID, URL: frURL, ExpiresAt: "2026-11-16T10:15:00Z"}
	for state, want := range map[string]string{
		"pending":  "is pending; it can be filled until 2026-11-16T10:15:00Z.",
		"filled":   "is filled: the file is added, and you will not see its content.",
		"declined": "was declined by the person.",
		"expired":  "has expired without a file.",
	} {
		text := fileRequestKind.summary(created, "tls.pem", state, elicitDeclined)
		if !strings.Contains(text, want) || !strings.Contains(text, frURL) {
			t.Errorf("%s: %s", state, text)
		}
		if (state == "pending") != strings.Contains(text, "stays open until it expires") {
			t.Errorf("%s: open sentence %s", state, text)
		}
	}
	for state, want := range map[string]string{
		"filled": "tls.pem: filled. The file is added; you will not see its content.", "declined": "tls.pem: declined by the person.",
		"expired": "tls.pem: expired without a file. Ask again with request_file if it is still needed.",
	} {
		if got := (fileRequestView{name: "tls.pem", state: state}).summary(); got != want {
			t.Errorf("%s: %s", state, got)
		}
	}
}

// TestReadinessNextAddFile: check_readiness names request_file for add_file.
func TestReadinessNextAddFile(t *testing.T) {
	got := nextStep(map[string]any{"action": "add_file", "name": "tls.pem"}, vrProject, "web")
	if got != "call request_file with project_id "+vrProject+" and name tls.pem; the person adds the file in the browser." {
		t.Fatalf("next %q", got)
	}
}
