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
// elicitation.url (api-v1 v1.31 item 10). On 2025-11-25 and on 2026-07-28 the client
// gets the link as text in one answer with one create and no URL input
// request (DD 8hp8). A token-shaped field the fake API adds
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
			if res.IsError || elicited {
				t.Fatalf("error %v elicited %v: %s", res.IsError, elicited, text)
			}
			if !strings.Contains(text, "https://console.example/connect/"+connectRequestID) || !strings.Contains(text, "No registry token is ever sent through this tool or shown to you.") {
				t.Fatalf("text %s", text)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			want := `"elicitation":"not_supported"`
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
			assertLast(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/registries/github/requests", "", `{"reason":"Private image"}`)
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
