package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectRequestID is the fake API's connect request id (43 characters).
const connectRequestID = "Connectreqid0123456789abcdefghijklmnopq_-AB"

// TestConnectRegistryThroughGateway runs connect_registry through the
// production (stateless) gateway with an SDK client that declares
// elicitation.url (api-v1 v1.31 item 10). On 2025-11-25 the client gets the
// link as text only. On 2026-07-28 it also gets a URL input request
// (SEP-2322); its retry reads the state without a second create, and the
// final text still carries the link. A token-shaped field the fake API adds
// is never passed on.
func TestConnectRegistryThroughGateway(t *testing.T) {
	if len(connectRequestID) != 43 {
		t.Fatalf("id length %d", len(connectRequestID))
	}
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			fake := &fakeAPI{token: userToken}
			mcpSrv, logs := newTestServer(t, fake)
			elicited := false
			client := mcp.NewClient(&mcp.Implementation{Name: "url-client", Version: "0"}, &mcp.ClientOptions{
				Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
				ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					elicited = req.Params.URL == "https://console.example/connect/"+connectRequestID
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
			res := mustCall(t, cs, "connect_registry", map[string]any{"project_id": projectID, "registry": "ghcr.io", "reason": "Private image"})
			text := textOf(t, res)
			newProtocol := protocol == "2026-07-28"
			if res.IsError || elicited != newProtocol {
				t.Fatalf("error %v elicited %v: %s", res.IsError, elicited, text)
			}
			if !strings.Contains(text, "https://console.example/connect/"+connectRequestID) || !strings.Contains(text, "No registry token is ever sent through this tool or shown to you.") {
				t.Fatalf("text %s", text)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			want := `"elicitation":"not_supported"`
			if newProtocol {
				want = `"elicitation":"accepted"`
			}
			if !strings.Contains(string(raw), want) || strings.Contains(string(raw), readinessLeak) || strings.Contains(text, readinessLeak) {
				t.Fatalf("structured %s", raw)
			}
			posts := 0
			for _, c := range fake.snapshot() {
				if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/registries/github/requests") {
					posts++
				}
			}
			if posts != 1 {
				t.Fatalf("creates %d", posts)
			}
			if newProtocol {
				assertLast(t, fake, http.MethodGet, "/v1/registry-connect-requests/"+connectRequestID, "", "")
			} else {
				assertLast(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/registries/github/requests", "", `{"reason":"Private image"}`)
			}
			res = mustCall(t, cs, "get_registry_connect_request", map[string]any{"request_id": connectRequestID})
			raw, _ = json.Marshal(res.StructuredContent)
			if res.IsError || strings.Contains(string(raw), readinessLeak) || textOf(t, res) != "ghcr.io: pending. The person has not connected GitHub yet." {
				t.Fatalf("get %s %s", raw, textOf(t, res))
			}
			if strings.Contains(logs.String(), connectRequestID) || strings.Contains(logs.String(), readinessLeak) {
				t.Fatal("the request id or a token is in the gateway log")
			}
		})
	}
}
