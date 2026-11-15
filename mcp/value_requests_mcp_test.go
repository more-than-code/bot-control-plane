package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// valueRequestID is the fake API's value request id (43 characters).
const valueRequestID = "Zyxwvutsrqponmlkjihgfedcba9876543210_-ZYXWV"

// TestRequestEnvValueThroughGateway runs request_env_value through the
// production (stateless) gateway with a client that declares
// elicitation.url (spec-env-request-value rev 2, AC 6). On 2025-11-25 the
// client gets the link as text only. On 2026-07-28 it also gets a URL input
// request (SEP-2322); its retry reads the state without a second create,
// and the final text still carries the link.
func TestRequestEnvValueThroughGateway(t *testing.T) {
	if len(valueRequestID) != 43 {
		t.Fatalf("id length %d", len(valueRequestID))
	}
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			fake := &fakeAPI{token: userToken}
			mcpSrv, logs := newTestServer(t, fake)
			elicited := false
			client := mcp.NewClient(&mcp.Implementation{Name: "url-client", Version: "0"}, &mcp.ClientOptions{
				Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
				ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					elicited = true
					return &mcp.ElicitResult{Action: "accept"}, nil
				},
			})
			cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:             mcpSrv.URL + "/mcp",
				HTTPClient:           &http.Client{Transport: &authRT{base: http.DefaultTransport, token: userToken, log: &tripLog{}}},
				DisableStandaloneSSE: true,
				MaxRetries:           -1,
			}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			t.Cleanup(func() { _ = cs.Close() })
			res := mustCall(t, cs, "request_env_value", map[string]any{"project_id": projectID, "set": "services", "key": "STRIPE_API_KEY"})
			text := textOf(t, res)
			newProtocol := protocol == "2026-07-28"
			if res.IsError || elicited != newProtocol {
				t.Fatalf("error %v elicited %v: %s", res.IsError, elicited, text)
			}
			if !strings.Contains(text, "https://console.example/fill/"+valueRequestID) || !strings.Contains(text, "Secret values are never sent through this tool or shown to you.") {
				t.Fatalf("text %s", text)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			want := `"elicitation":"not_supported"`
			if newProtocol {
				want = `"elicitation":"accepted"`
			}
			if !strings.Contains(string(raw), want) {
				t.Fatalf("structured %s", raw)
			}
			posts := 0
			for _, c := range fake.snapshot() {
				if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/value-requests") {
					posts++
				}
			}
			if posts != 1 {
				t.Fatalf("creates %d", posts)
			}
			if newProtocol {
				assertLast(t, fake, http.MethodGet, "/v1/value-requests/"+valueRequestID, "", "")
			} else {
				assertLast(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/env-sets/services/value-requests", "", `{"key":"STRIPE_API_KEY"}`)
			}
			if strings.Contains(logs.String(), valueRequestID) {
				t.Fatal("the request id is in the gateway log")
			}
		})
	}
}
