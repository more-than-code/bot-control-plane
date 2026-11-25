package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fileRequestID is the fake API's file request id (43 characters).
const fileRequestID = "Fyxwvutsrqponmlkjihgfedcba9876543210_-FILES"

// TestRequestFileThroughGateway runs request_file through the production
// (stateless) gateway with a client that declares elicitation.url
// (spec-file-request AC 6). On 2025-11-25 and on 2026-07-28 the client
// gets the link as text in one answer with one create and no URL input
// request (DD 8hp8).
func TestRequestFileThroughGateway(t *testing.T) {
	if len(fileRequestID) != 43 {
		t.Fatalf("id length %d", len(fileRequestID))
	}
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			fake := &fakeAPI{token: userToken}
			mcpSrv, logs := newTestServer(t, fake)
			var elicited *mcp.ElicitParams
			client := mcp.NewClient(&mcp.Implementation{Name: "url-client", Version: "0"}, &mcp.ClientOptions{
				Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}},
				ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					elicited = req.Params
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
			res := mustCall(t, cs, "request_file", map[string]any{"project_id": projectID, "name": "nginx.conf", "reason": "the proxy config"})
			text := textOf(t, res)
			link := "https://console.example/fill-file/" + fileRequestID
			if res.IsError || elicited != nil {
				t.Fatalf("error %v elicited %v: %s", res.IsError, elicited, text)
			}
			if !strings.Contains(text, link) || !strings.Contains(text, "File contents are never sent through this tool or shown to you.") {
				t.Fatalf("text %s", text)
			}
			raw, _ := json.Marshal(res.StructuredContent)
			want := `"elicitation":"not_supported"`
			if !strings.Contains(string(raw), want) || strings.Contains(string(raw), "content") {
				t.Fatalf("structured %s", raw)
			}
			posts := 0
			for _, c := range fake.snapshot() {
				if c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/file-requests") {
					posts++
				}
			}
			if posts != 1 {
				t.Fatalf("creates %d", posts)
			}
			assertLast(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/file-requests", "", `{"name":"nginx.conf","reason":"the proxy config"}`)
			if strings.Contains(logs.String(), fileRequestID) {
				t.Fatal("the request id is in the gateway log")
			}
			res = mustCall(t, cs, "get_file_request", map[string]any{"request_id": fileRequestID})
			if res.IsError || textOf(t, res) != "nginx.conf: pending. The person has not added the file yet." {
				t.Fatalf("get %s", textOf(t, res))
			}
		})
	}
}
