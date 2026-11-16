package agentws

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/edgeconfig"
)

// agent-protocol v1.15 item 2: hello programs.edge is accepted (an API
// that refused it would disable the whole programs object), and its image
// must be a repository pinned by digest.
func TestParseProgramsEdge(t *testing.T) {
	digest := "docker.io/library/caddy@sha256:" + strings.Repeat("b", 64)
	p, ok := parsePrograms([]byte(`{"enabled":true,"run_as":[],"edge":{"enabled":true,"image":"` + digest + `"}}`))
	if !ok || p.Edge == nil || !p.Edge.Enabled || p.Edge.Image != digest || !p.EdgeAvailable() {
		t.Fatalf("edge %#v %v", p, ok)
	}
	p, ok = parsePrograms([]byte(`{"enabled":true,"run_as":[]}`))
	if !ok || p.Edge != nil || p.EdgeAvailable() {
		t.Fatalf("absent edge %#v", p)
	}
	for _, bad := range []string{
		`{"enabled":true,"edge":{"enabled":true,"image":"caddy:2"}}`,
		`{"enabled":true,"edge":{"enabled":true,"image":"caddy@sha256:abc"}}`,
		`{"enabled":true,"edge":{"enabled":true,"extra":1}}`,
	} {
		if _, ok := parsePrograms([]byte(bad)); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, ok := parsePrograms([]byte(`{"enabled":true,"edge":{"enabled":false,"image":""}}`)); !ok {
		t.Fatal("refused an empty image")
	}
}

func TestEdgeRejectCodes(t *testing.T) {
	for code, want := range map[string]string{"edge_disabled": "forbidden", "port_in_use": "conflict", "invalid_config": "bad_request"} {
		if got, _ := rejectCode(code); got != want {
			t.Fatalf("%s → %s", code, got)
		}
		if ce := failedTask(code, []byte(`{"holder":"x"}`)); ce.Code != want || string(ce.Payload) != `{"holder":"x"}` {
			t.Fatalf("failed %s → %#v", code, ce)
		}
	}
}

// TestEdgeApplyFrameHash is the cross-slice check: the edge_apply frame the
// hub writes carries the config bytes unchanged, so sha256 of the "config"
// value as received equals config_hash (what the agent verifies).
func TestEdgeApplyFrameHash(t *testing.T) {
	cfg, hash, err := edgeconfig.Build([]edgeconfig.Route{
		{Hostname: "defectdrainer.com", PathPrefix: "/", Upstream: 8788, CloudflareClientIP: true, NoStorePaths: []string{"/evidence/"}},
		{Hostname: "daikou.online", PathPrefix: "/", Upstream: 3000},
	}, edgeconfig.Options{ACMEEmail: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// The spec as api.edgeApplySpec builds it, in the frame hub.live writes.
	frame := taskFrame{V: 1, Type: "task", TaskID: uuid.New(), HostID: uuid.New(), Kind: "edge_apply",
		ExpiresAt: "2026-11-17T00:00:00Z", TimeoutSec: EdgeTimeoutSec,
		Spec: map[string]any{"config": json.RawMessage(cfg), "config_hash": hash}}
	wire, err := (&agentConn{}).marshalFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Spec struct {
			Config     json.RawMessage `json:"config"`
			ConfigHash string          `json:"config_hash"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got.Spec.Config)
	if hex.EncodeToString(sum[:]) != got.Spec.ConfigHash || got.Spec.ConfigHash != hash || !bytes.Equal(got.Spec.Config, cfg) {
		t.Fatalf("wire config hash %x, sent %s", sum, got.Spec.ConfigHash)
	}
}
