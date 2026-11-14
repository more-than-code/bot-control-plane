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

// api-v1 v1.25 item 6: networks in the write schema and local validation.
func TestNetworksSchema(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(catalogWriteSchema), &schema); err != nil {
		t.Fatal(err)
	}
	props, _ := schema["properties"].(map[string]any)
	spec, _ := props["spec"].(map[string]any)
	specProps, _ := spec["properties"].(map[string]any)
	nets, ok := specProps["networks"].(map[string]any)
	if !ok {
		t.Fatal("networks missing from spec schema")
	}
	if nets["minItems"] != float64(1) || nets["maxItems"] != float64(4) || nets["uniqueItems"] != true {
		t.Fatalf("networks %#v", nets)
	}
	items, _ := nets["items"].(map[string]any)
	if items["type"] != "string" || items["pattern"] != "^[a-z0-9][a-z0-9-]{0,31}$" {
		t.Fatalf("items %#v", items)
	}
	if _, ok := imageSpecKeys["networks"]; !ok {
		t.Fatal("networks not in imageSpecKeys")
	}
}

func TestNetworksShape(t *testing.T) {
	ok := []string{
		`{"kind":"service","image":"nginx","networks":["dd"]}`,
		`{"kind":"job","image":"nginx","networks":["a","b-2","c","d"]}`,
		`{"kind":"service","image":"nginx","networks":null}`,
		`{"kind":"service","image":"nginx"}`,
	}
	for _, spec := range ok {
		if err := validateImageSpec(json.RawMessage(spec)); err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
	}
	bad := []string{
		`[]`,
		`["DD"]`,
		`["dd","dd"]`,
		`["a","b","c","d","e"]`,
		`["` + strings.Repeat("a", 33) + `"]`,
		`"dd"`,
		`[1]`,
	}
	for _, nets := range bad {
		err := validateImageSpec(json.RawMessage(`{"kind":"service","image":"nginx","networks":` + nets + `}`))
		if err == nil || !strings.HasPrefix(err.Error(), "networks") {
			t.Fatalf("%s: %v", nets, err)
		}
	}
}

func TestProjectCatalogNetworks(t *testing.T) {
	raw := []byte(`{"items":[
		{"name":"serve","kind":"image","command_display":"dd:1",
		 "spec":{"kind":"service","image":"dd:1","networks":["dd"]},"my_actions":["deploy"]},
		{"name":"bare","kind":"image","command_display":"nginx:1",
		 "spec":{"kind":"service","image":"nginx:1"},"my_actions":["deploy"]}
	]}`)
	out, err := projectCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &body); err != nil || len(body.Items) != 2 {
		t.Fatalf("%s %v", out, err)
	}
	if got, _ := body.Items[0]["networks"].([]any); len(got) != 1 || got[0] != "dd" {
		t.Fatalf("serve networks %#v", body.Items[0]["networks"])
	}
	// Amendment A4: null, not [], so the entry can be written back.
	if v, ok := body.Items[1]["networks"]; !ok || v != nil {
		t.Fatalf("bare networks %#v", body.Items[1]["networks"])
	}
	if err := validateImageSpec(json.RawMessage(`{"kind":"service","image":"nginx","networks":null}`)); err != nil {
		t.Fatalf("listed null networks not writable: %v", err)
	}
}

func TestSummarizeHostsNetworks(t *testing.T) {
	payload := json.RawMessage(`{"items":[
		{"name":"daikou","status":"online","access_level":"deploy","programs":{"shared_networks":[{"name":"dd","subnet":"10.200.10.0/24"},{"name":"edge","subnet":"10.200.11.0/24","projects":["x"]}]}},
		{"name":"pi1","status":"online","access_level":"deploy","programs":{"shared_networks":[]}},
		{"name":"pi2","status":"offline","programs":{"shared_networks":null}},
		{"name":"pi3","status":"online","programs":null}
	]}`)
	want := "4 hosts: daikou online (deploy) (shared networks: dd 10.200.10.0/24, edge 10.200.11.0/24), pi1 online (deploy), pi2 offline, pi3 online"
	if got := summarizeHosts(payload, false); got != want {
		t.Fatalf("got %q", got)
	}
}

// TestNetworksSchemaAllowsNull: the schema type is ["array","null"].
func TestNetworksSchemaAllowsNull(t *testing.T) {
	var schema struct {
		Properties struct {
			Spec struct {
				Properties map[string]struct {
					Type json.RawMessage `json:"type"`
				} `json:"properties"`
			} `json:"spec"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(catalogWriteSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if got := string(schema.Properties.Spec.Properties["networks"].Type); got != `["array", "null"]` {
		t.Fatalf("networks type %v", got)
	}
}

// callWriteTool runs one tool over an in-memory session against a fake API
// and returns the result and the request bodies the API received.
func callWriteTool(t *testing.T, name string, args map[string]any) (*mcp.CallToolResult, []apiRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []apiRequest
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, apiRequest{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"code":"ok","msg":"ok","payload":{"confirmation_id":"Abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE","plan":"will create image entry web","expires_at":"2026-11-15T00:05:00Z"}}`)
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
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return res, append([]apiRequest{}, seen...)
}

type apiRequest struct {
	Method, Path, Body string
}

// TestCatalogWriteToolsForwardNetworks: create and update forward
// spec.networks unchanged, and a bad list is refused before any API call.
func TestCatalogWriteToolsForwardNetworks(t *testing.T) {
	const project = "8f1c2d3e-4b5a-4c6d-8e7f-901234567890"
	spec := map[string]any{"kind": "service", "image": "nginx", "networks": []string{"edge", "dd"}}
	for _, tc := range []struct {
		tool, method, path string
	}{
		{"create_catalog_entry", http.MethodPost, "/v1/projects/" + project + "/catalog"},
		{"update_catalog_entry", http.MethodPut, "/v1/projects/" + project + "/catalog/web"},
	} {
		res, seen := callWriteTool(t, tc.tool, map[string]any{"project_id": project, "name": "web", "idempotency_key": "net-1", "spec": spec})
		if res.IsError {
			t.Fatalf("%s: %s", tc.tool, toolText(t, res))
		}
		if len(seen) != 1 || seen[0].Method != tc.method || seen[0].Path != tc.path {
			t.Fatalf("%s requests %#v", tc.tool, seen)
		}
		var body struct {
			Spec map[string]json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal([]byte(seen[0].Body), &body); err != nil {
			t.Fatal(err)
		}
		if string(body.Spec["networks"]) != `["edge","dd"]` {
			t.Fatalf("%s forwarded %s", tc.tool, seen[0].Body)
		}

		bad := map[string]any{"kind": "service", "image": "nginx", "networks": []string{"dd", "dd"}}
		res, seen = callWriteTool(t, tc.tool, map[string]any{"project_id": project, "name": "web", "idempotency_key": "net-2", "spec": bad})
		if !res.IsError || len(seen) != 0 {
			t.Fatalf("%s bad list error=%v requests %d", tc.tool, res.IsError, len(seen))
		}
	}
}

func TestSummarizeHostsNetworksOrg(t *testing.T) {
	payload := json.RawMessage(`{"items":[
		{"name":"daikou","status":"online","programs":{"shared_networks":[{"name":"dd","subnet":"10.200.10.0/24","projects":["daikou","defect-drainer"]}]}},
		{"name":"pi1","status":"online","programs":{"shared_networks":[]}}
	]}`)
	got := summarizeHosts(payload, true)
	if !strings.Contains(got, "daikou online") || !strings.Contains(got, "(shared networks: dd 10.200.10.0/24)") || strings.Contains(got, "defect-drainer") {
		t.Fatalf("org summary %q", got)
	}
	if strings.Contains(got, "pi1 online (shared") {
		t.Fatalf("empty list shown %q", got)
	}
}

// A hostile or oversized name or subnet goes through summaryField: controls
// and bidi marks are replaced and the field is capped.
func TestSummarizeHostsNetworksHostile(t *testing.T) {
	long := strings.Repeat("n", summaryFieldRunes+50)
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"name": "h", "status": "online",
		"programs": map[string]any{"shared_networks": []any{
			map[string]any{"name": "dd\nforged line‮", "subnet": long},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got := summarizeHosts(raw, false)
	if strings.ContainsAny(got, "\n‮") {
		t.Fatalf("control kept %q", got)
	}
	if !strings.Contains(got, summaryField("dd\nforged line‮")+" "+summaryField(long)) {
		t.Fatalf("not through summaryField %q", got)
	}
	if strings.Contains(got, long) {
		t.Fatal("oversized subnet not capped")
	}
}

// TestMaxCatalogPlanRunesSum pins the sizing table in summary.go: the
// v1.25 shared networks row adds 290 runes (checked against the API plan
// builder by api TestNetworksPlanWorstCase).
func TestMaxCatalogPlanRunesSum(t *testing.T) {
	if maxCatalogPlanRunes != 2101954+2097559+78955+290+145 {
		t.Fatalf("maxCatalogPlanRunes %d does not match its table", maxCatalogPlanRunes)
	}
	exact := strings.Repeat("q", maxCatalogPlanRunes)
	ok, err := json.Marshal(map[string]any{"confirmation_id": "Abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE", "plan": exact, "expires_at": "2026-11-15T00:05:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if res := confirmationCatalog(http.StatusAccepted, ok); res.IsError {
		t.Fatalf("plan at the cap refused: %s", toolText(t, res))
	}
}
