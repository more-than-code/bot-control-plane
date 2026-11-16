package edgeconfig

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, got, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Fatalf("%s differs:\n%s", name, pretty.String())
	}
}

// daikou is the spec §7 cutover: daikou.online plain, defectdrainer.com
// with both route options.
func daikou() []Route {
	return []Route{
		{Hostname: "defectdrainer.com", PathPrefix: "/", Upstream: 8788, CloudflareClientIP: true, NoStorePaths: []string{"/evidence/"}},
		{Hostname: "daikou.online", PathPrefix: "/", Upstream: 3000},
	}
}

func TestBuildGolden(t *testing.T) {
	raw, hash, err := Build(daikou(), Options{ACMEEmail: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "daikou.json", raw)
	if len(hash) != 64 {
		t.Fatalf("hash %q", hash)
	}
	raw, _, err = Build([]Route{
		{Hostname: "app.example.com", PathPrefix: "/", Upstream: 3000},
		{Hostname: "app.example.com", PathPrefix: "/api", Upstream: 8080, NoStorePaths: []string{"/api/private", "/api/x/"}},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "paths.json", raw)
	raw, _, err = Build(nil, Options{ACMEEmail: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "empty.json", raw)
	// BOT_API_EDGE_TLS_ISSUER=internal: Caddy's own CA, the email ignored.
	raw, _, err = Build(daikou(), Options{ACMEEmail: "ops@example.com", InternalIssuer: true})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "daikou-internal.json", raw)
}

// The default (ACME) output must not change: its config_hash is what
// existing edges have applied. The pinned hashes are of the bytes the
// golden files were made from.
func TestBuildDefaultHashPinned(t *testing.T) {
	for _, tc := range []struct {
		opt  Options
		want string
	}{
		{Options{ACMEEmail: "ops@example.com"}, daikouHash},
		{Options{}, daikouNoEmailHash},
	} {
		_, hash, err := Build(daikou(), tc.opt)
		if err != nil || hash != tc.want {
			t.Fatalf("hash %s %v, want %s", hash, err, tc.want)
		}
	}
	_, internal, _ := Build(daikou(), Options{InternalIssuer: true})
	if internal == daikouHash || internal == daikouNoEmailHash {
		t.Fatal("the internal issuer did not change the hash")
	}
}

// The agent accepts only admin (listen on the socket), optionally
// config.persist, and apps at the top level.
func TestBuildTopLevelKeys(t *testing.T) {
	for _, opt := range []Options{{}, {ACMEEmail: "ops@example.com"}, {InternalIssuer: true}} {
		for _, routes := range [][]Route{nil, daikou()} {
			raw, _, err := Build(routes, opt)
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(raw, &top); err != nil {
				t.Fatal(err)
			}
			if len(top) != 2 || top["admin"] == nil || top["apps"] == nil {
				t.Fatalf("top-level keys %s", raw)
			}
			var admin map[string]any
			if err := json.Unmarshal(top["admin"], &admin); err != nil {
				t.Fatal(err)
			}
			if len(admin) != 1 || admin["listen"] != "unix//run/edge/admin.sock" {
				t.Fatalf("admin %s", top["admin"])
			}
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	a, ha, _ := Build(daikou(), Options{})
	routes := daikou()
	routes[0], routes[1] = routes[1], routes[0]
	b, hb, _ := Build(routes, Options{})
	if !bytes.Equal(a, b) || ha != hb {
		t.Fatal("route order changed the config")
	}
	_, hc, _ := Build(daikou(), Options{ACMEEmail: "x@example.com"})
	if hc == ha {
		t.Fatal("the ACME email did not change the hash")
	}
}

func TestBuildShape(t *testing.T) {
	raw, _, err := Build(daikou(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`"listen":"unix//run/edge/admin.sock"`,
		`"dial":"127.0.0.1:8788"`,
		`"dial":"127.0.0.1:3000"`,
		`"Cache-Control":["private, no-store"]`,
		`"path":["/evidence/*"]`,
		`"X-Forwarded-For":["{http.request.header.CF-Connecting-IP}"]`,
		`"prefer":["zstd","gzip"]`,
		`"subjects":["daikou.online","defectdrainer.com"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %s", want)
		}
	}
	if strings.Contains(text, "issuers") {
		t.Fatal("issuers without an ACME email")
	}
	// daikou.online has no Cloudflare matcher: exactly one remote_ip.
	if strings.Count(text, "remote_ip") != 1 || strings.Count(text, "no-store") != 1 {
		t.Fatal("route options leaked to another route")
	}
	if strings.Contains(text, ":2019") || strings.Contains(text, "tcp/") {
		t.Fatal("admin on TCP")
	}
	// Only a Cloudflare peer that sent CF-Connecting-IP is trusted; anyone
	// else (or no header) falls through to the peer address.
	if !strings.Contains(text, `"header":{"Cf-Connecting-Ip":["*"]}`) || strings.Contains(text, "terminal") {
		t.Fatalf("cloudflare branch %s", text)
	}
}

func TestBuildTooLarge(t *testing.T) {
	var routes []Route
	for i := 0; i < 4000; i++ {
		routes = append(routes, Route{Hostname: strings.Repeat("a", 60) + ".example.com", PathPrefix: "/p" + strings.Repeat("x", 40) + string(rune('a'+i%26)) + "/" + strings.Repeat("y", i%50+1), Upstream: 1000 + i})
	}
	if _, _, err := Build(routes, Options{}); err != ErrTooLarge {
		t.Fatalf("err %v", err)
	}
}

func TestCloudflareRanges(t *testing.T) {
	got := CloudflareRanges()
	if len(got) < 20 {
		t.Fatalf("%d ranges", len(got))
	}
	for _, r := range got {
		p, err := netip.ParsePrefix(r)
		if err != nil || p.Masked() != p {
			t.Fatalf("range %s", r)
		}
	}
	got[0] = "changed"
	if CloudflareRanges()[0] == "changed" {
		t.Fatal("the list is shared")
	}
}

// Hashes of the daikou routes (the same bytes as testdata/daikou.json,
// compacted). They changed once, before any release, when "terminal" was
// dropped and the Cloudflare branch gained its header matcher (review 11);
// a change here re-applies every edge.
const (
	daikouHash        = "d10d8d0ec59908b3f747d90289f61f7ffd399c5d00267d716080c69197ffc0d3"
	daikouNoEmailHash = "fa2eb88b8822c63954f2c1802abf7a2673a91af3d7b67cd0fb86418b658313d9"
)

// TestBuildAllowlist walks the config and fails on any key outside the
// shape the agent validates (agent-protocol v1.15): top level admin and
// apps; apps http and tls; one server with listen [":443"] and routes;
// routes with match and handle; matchers host, path, remote_ip.ranges and
// header; handlers subroute, headers, encode and reverse_proxy.
func TestBuildAllowlist(t *testing.T) {
	routes := append(daikou(), Route{Hostname: "daikou.online", PathPrefix: "/api", Upstream: 4000, NoStorePaths: []string{"/api/x"}})
	for _, opt := range []Options{{}, {ACMEEmail: "ops@example.com"}, {InternalIssuer: true}} {
		raw, _, err := Build(routes, opt)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		keys := func(m map[string]any, allowed ...string) {
			t.Helper()
			ok := map[string]bool{}
			for _, k := range allowed {
				ok[k] = true
			}
			for k := range m {
				if !ok[k] {
					t.Fatalf("key %s not in %v", k, allowed)
				}
			}
		}
		keys(cfg, "admin", "apps")
		apps := cfg["apps"].(map[string]any)
		keys(apps, "http", "tls")
		servers := apps["http"].(map[string]any)["servers"].(map[string]any)
		server := servers["edge"].(map[string]any)
		keys(server, "listen", "routes")
		if l := server["listen"].([]any); len(l) != 1 || l[0] != ":443" {
			t.Fatalf("listen %v", l)
		}
		var walk func(routes []any)
		walk = func(routes []any) {
			for _, r := range routes {
				route := r.(map[string]any)
				keys(route, "match", "handle")
				matches, _ := route["match"].([]any)
				for _, m := range matches {
					keys(m.(map[string]any), "host", "path", "remote_ip", "header")
				}
				for _, h := range route["handle"].([]any) {
					handler := h.(map[string]any)
					switch handler["handler"] {
					case "subroute":
						keys(handler, "handler", "routes")
						walk(handler["routes"].([]any))
					case "headers":
						keys(handler, "handler", "response")
					case "encode":
						keys(handler, "handler", "encodings", "prefer")
					case "reverse_proxy":
						keys(handler, "handler", "upstreams", "headers")
						up := handler["upstreams"].([]any)
						dial := up[0].(map[string]any)["dial"].(string)
						if len(up) != 1 || !strings.HasPrefix(dial, "127.0.0.1:") {
							t.Fatalf("upstreams %v", up)
						}
					default:
						t.Fatalf("handler %v", handler["handler"])
					}
				}
			}
		}
		walk(server["routes"].([]any))
		if tls, ok := apps["tls"].(map[string]any); ok {
			keys(tls, "automation")
			for _, p := range tls["automation"].(map[string]any)["policies"].([]any) {
				policy := p.(map[string]any)
				keys(policy, "subjects", "issuers")
				issuers, _ := policy["issuers"].([]any)
				for _, i := range issuers {
					issuer := i.(map[string]any)
					switch issuer["module"] {
					case "internal":
						keys(issuer, "module")
					case "acme":
						keys(issuer, "module", "email")
					default:
						t.Fatalf("issuer %v", issuer)
					}
				}
			}
		}
	}
}
