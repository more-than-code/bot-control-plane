// Package edgeconfig builds the Caddy JSON config a host's edge loads
// (api-v1 v1.35 item 5, agent-protocol v1.15 item 4). The output is
// deterministic: the same routes give the same bytes, so the config hash
// only changes when the config does.
package edgeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// MaxBytes is the largest config an edge_apply task may carry.
const MaxBytes = 512 << 10

// AdminListen is Caddy's admin endpoint: a unix socket in the config
// directory the agent bind-mounts at /run/edge. It is never TCP.
const AdminListen = "unix//run/edge/admin.sock"

// ErrTooLarge is a config over MaxBytes.
var ErrTooLarge = errors.New("the edge config is too large")

// Route is one live route on the host. Upstream is the host port on
// 127.0.0.1 (an image entry's published port, or a program's port).
type Route struct {
	Hostname           string
	PathPrefix         string
	Upstream           int
	CloudflareClientIP bool
	NoStorePaths       []string
}

// Options are per-host settings.
type Options struct {
	// ACMEEmail is the org's ACME contact; "" leaves Caddy's default issuers.
	ACMEEmail string
	// InternalIssuer makes every TLS policy use Caddy's internal CA instead
	// of ACME, ignoring ACMEEmail (BOT_API_EDGE_TLS_ISSUER=internal, dev and
	// test hosts only).
	InternalIssuer bool
}

// Build returns the config bytes and their sha256 (hex).
func Build(routes []Route, opt Options) ([]byte, string, error) {
	sorted := append([]Route(nil), routes...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Hostname != b.Hostname {
			return a.Hostname < b.Hostname
		}
		// Longer prefixes first, so /api wins over / for the same host.
		if len(a.PathPrefix) != len(b.PathPrefix) {
			return len(a.PathPrefix) > len(b.PathPrefix)
		}
		return a.PathPrefix < b.PathPrefix
	})
	httpRoutes := make([]any, 0, len(sorted))
	subjects := []string{}
	seen := map[string]bool{}
	for _, r := range sorted {
		httpRoutes = append(httpRoutes, hostRoute(r))
		if !seen[r.Hostname] {
			seen[r.Hostname] = true
			subjects = append(subjects, r.Hostname)
		}
	}
	apps := map[string]any{
		"http": map[string]any{
			"servers": map[string]any{
				// Listening on 443 only: Caddy's automatic HTTPS gets a
				// certificate for every host matcher, answers the ACME HTTP
				// challenge and redirects HTTP to HTTPS on port 80.
				"edge": map[string]any{
					"listen": []string{":443"},
					"routes": httpRoutes,
				},
			},
		},
	}
	if len(subjects) > 0 {
		policy := map[string]any{"subjects": subjects}
		switch {
		case opt.InternalIssuer:
			policy["issuers"] = []any{map[string]any{"module": "internal"}}
		case opt.ACMEEmail != "":
			policy["issuers"] = []any{map[string]any{"module": "acme", "email": opt.ACMEEmail}}
		}
		apps["tls"] = map[string]any{"automation": map[string]any{"policies": []any{policy}}}
	}
	cfg := map[string]any{
		"admin": map[string]any{"listen": AdminListen},
		"apps":  apps,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	if len(raw) > MaxBytes {
		return nil, "", ErrTooLarge
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// pathMatch is the Caddy path matcher for a prefix: "/" matches all (nil),
// "/a/" matches below it, "/a" matches itself and below it.
func pathMatch(prefix string) []string {
	switch {
	case prefix == "" || prefix == "/":
		return nil
	case strings.HasSuffix(prefix, "/"):
		return []string{prefix + "*"}
	default:
		return []string{prefix, prefix + "/*"}
	}
}

func hostRoute(r Route) map[string]any {
	match := map[string]any{"host": []string{r.Hostname}}
	if paths := pathMatch(r.PathPrefix); paths != nil {
		match["path"] = paths
	}
	inner := []any{}
	if len(r.NoStorePaths) > 0 {
		var paths []string
		for _, p := range r.NoStorePaths {
			if m := pathMatch(p); m != nil {
				paths = append(paths, m...)
			}
		}
		sort.Strings(paths)
		if len(paths) > 0 {
			inner = append(inner, map[string]any{
				"match": []any{map[string]any{"path": paths}},
				"handle": []any{map[string]any{
					"handler": "headers",
					// Deferred so it replaces whatever the upstream sent.
					"response": map[string]any{
						"set":      map[string]any{"Cache-Control": []string{"private, no-store"}},
						"deferred": true,
					},
				}},
			})
		}
	}
	inner = append(inner, map[string]any{
		"handle": []any{map[string]any{
			"handler":   "encode",
			"encodings": map[string]any{"zstd": map[string]any{}, "gzip": map[string]any{}},
			"prefer":    []string{"zstd", "gzip"},
		}},
	})
	dial := "127.0.0.1:" + strconv.Itoa(r.Upstream)
	if r.CloudflareClientIP {
		// From Cloudflare with CF-Connecting-IP set: that is the client.
		// From anyone else, or without the header: Caddy sets
		// X-Forwarded-For from the peer (no trusted proxies are configured,
		// so a sent one is dropped) and the Cloudflare headers are removed.
		// reverse_proxy answers the request, so the first matching route
		// ends it; no "terminal" flag is needed.
		inner = append(inner,
			map[string]any{
				"match": []any{map[string]any{
					"remote_ip": map[string]any{"ranges": CloudflareRanges()},
					"header":    map[string]any{"Cf-Connecting-Ip": []string{"*"}},
				}},
				"handle": []any{proxy(dial, map[string]any{"set": map[string]any{"X-Forwarded-For": []string{"{http.request.header.CF-Connecting-IP}"}}})},
			},
			map[string]any{
				"handle": []any{proxy(dial, map[string]any{"delete": []string{"Cf-Connecting-Ip", "True-Client-Ip", "X-Real-Ip"}})},
			},
		)
	} else {
		inner = append(inner, map[string]any{"handle": []any{proxy(dial, nil)}})
	}
	// Host routes are ordered longest prefix first, and each ends in
	// reverse_proxy, which answers: the first match wins without "terminal".
	return map[string]any{
		"match":  []any{match},
		"handle": []any{map[string]any{"handler": "subroute", "routes": inner}},
	}
}

// streamCloseDelay keeps WebSockets of the old config open for up to an hour
// after a reload; without it Caddy closes them (1001) on every config change
// (spec-domains-edge-routes rev 3 §11). The agent accepts only this value.
const streamCloseDelay = "1h"

// proxy is reverse_proxy to dial. Caddy proxies WebSockets and sets
// X-Forwarded-For, -Proto and -Host itself.
func proxy(dial string, requestHeaders map[string]any) map[string]any {
	h := map[string]any{
		"handler":            "reverse_proxy",
		"upstreams":          []any{map[string]any{"dial": dial}},
		"stream_close_delay": streamCloseDelay,
	}
	if requestHeaders != nil {
		h["headers"] = map[string]any{"request": requestHeaders}
	}
	return h
}
