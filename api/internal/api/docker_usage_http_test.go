package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// dockerSummary is a docker_usage summary as agent-protocol v1.25 sends it.
var dockerSummary = map[string]any{
	"totals": map[string]any{
		"images":      map[string]any{"count": 3, "active": 1, "size": 3000, "reclaimable": 2000},
		"containers":  map[string]any{"count": 2, "active": 1, "size": 40, "reclaimable": 10},
		"volumes":     map[string]any{"count": 1, "active": 1, "size": 500, "reclaimable": 0},
		"build_cache": map[string]any{"count": 0, "active": 0, "size": 0, "reclaimable": 0},
	},
	"images":     []any{map[string]any{"ref": "ghcr.io/o/app:1.2", "id": "0123456789ab", "size": 2000, "containers": 1, "labels": map[string]any{"secret": "x"}}},
	"volumes":    []any{map[string]any{"name": "pgdata", "size": nil, "in_use": true}},
	"containers": []any{map[string]any{"name": "deploybot-api", "state": "running", "size": 30}},
	"extra":      "dropped",
}

const dockerSummaryJSON = `{"at":"2026-10-13T12:00:00Z",` +
	`"containers":[{"name":"deploybot-api","size":30,"state":"running"}],` +
	`"images":[{"containers":1,"id":"0123456789ab","ref":"ghcr.io/o/app:1.2","size":2000}],` +
	`"totals":{"build_cache":{"active":0,"count":0,"reclaimable":0,"size":0},` +
	`"containers":{"active":1,"count":2,"reclaimable":10,"size":40},` +
	`"images":{"active":1,"count":3,"reclaimable":2000,"size":3000},` +
	`"volumes":{"active":1,"count":1,"reclaimable":0,"size":500}},` +
	`"volumes":[{"in_use":true,"name":"pgdata","size":null}]}`

// dockerAgent connects the fixture's host with version and answers each
// task with reply (nil: no answer). The tasks it saw go to tasks.
func dockerAgent(t *testing.T, fx *accessFixture, version string, reply func(task map[string]any) map[string]any) <-chan map[string]any {
	t.Helper()
	conn := fx.w.dial(fx.sc.credPlain)
	writeFrame(t, conn, map[string]any{
		"v": 1, "type": "hello", "host_id": fx.sc.host.ID.String(), "credential_id": fx.sc.credID.String(),
		"agent_version": version, "os": "linux", "arch": "arm64", "services": []any{},
	})
	if hello := readFrame(t, conn); hello["type"] != "hello_ok" {
		t.Fatalf("hello %#v", hello)
	}
	tasks := make(chan map[string]any, 32)
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) != nil || msg["type"] != "task" {
				continue
			}
			tasks <- msg
			out := reply(msg)
			if out == nil {
				continue
			}
			out["v"], out["task_id"] = 1, msg["task_id"]
			raw, _ := json.Marshal(out)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Write(ctx, websocket.MessageText, raw)
			cancel()
		}
	}()
	return tasks
}

func dockerOK(map[string]any) map[string]any {
	return map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"outcome": "ok", "summary": dockerSummary}}
}

func dockerAudits(w *world) []store.AuditEntry {
	var out []store.AuditEntry
	for _, a := range w.mem.AllAudits() {
		if a.Action == audit.ActionHostDockerUsage {
			out = append(out, a)
		}
	}
	return out
}

// TestDockerUsageWho: org owners and admins get the summary; project
// members with any access level get 403; a caller who cannot see the host
// (an org member without access, another org) gets 404.
func TestDockerUsageWho(t *testing.T) {
	fx := newAccessFixture(t)
	dockerAgent(t, fx, "0.2.17", dockerOK)
	const ok, no, miss = http.StatusOK, http.StatusForbidden, http.StatusNotFound
	want := cellWant(no, ok, no, no, no, miss)
	for _, actor := range accessActors {
		code, env := fx.do(actor, http.MethodPost, fx.hostPath("/docker-usage"), "")
		if code != want[actor] {
			t.Fatalf("%s: %d %s %s, want %d", actor, code, env.Code, env.Msg, want[actor])
		}
	}
	// A delegated (MCP) org owner is allowed too.
	tok := tokenKind(t, fx.w, fx.users[actOwnerOwner].ID, store.KindDelegated)
	code, env := fx.w.do(tok, http.MethodPost, fx.hostPath("/docker-usage"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	var denied int
	for _, a := range dockerAudits(fx.w) {
		if a.Decision == audit.DecisionDenied {
			denied++
		}
	}
	// Five 403s are audited (a hidden host's 404 is audited without the host).
	if denied < 5 {
		t.Fatalf("denied rows %d", denied)
	}
}

// TestDockerUsageSuccess: the task frame, the 200 payload (summary read
// field by field, plus at) and the allowed audit row.
func TestDockerUsageSuccess(t *testing.T) {
	fx := newAccessFixture(t)
	tasks := dockerAgent(t, fx, "0.2.17", dockerOK)
	code, env := fx.do(actOwnerAdmin, http.MethodPost, fx.hostPath("/docker-usage"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	raw, _ := json.Marshal(env.Payload)
	if string(raw) != dockerSummaryJSON {
		t.Fatalf("payload %s", raw)
	}
	task := <-tasks
	spec, _ := json.Marshal(task["spec"])
	if task["kind"] != "docker_usage" || string(spec) != "{}" || task["timeout_sec"] != float64(45) || task["service"] != nil {
		t.Fatalf("task %#v", task)
	}
	rows := dockerAudits(fx.w)
	if len(rows) != 1 || rows[0].Decision != audit.DecisionAllowed || rows[0].Result == nil || *rows[0].Result != "succeeded" ||
		rows[0].HostID == nil || *rows[0].HostID != fx.sc.host.ID || rows[0].Via != audit.ViaConsole {
		t.Fatalf("audit %+v", rows)
	}
}

// TestDockerUsageCaps: lists over ten are cut, long strings capped, an
// unknown size is null and a negative count is 0.
func TestDockerUsageCaps(t *testing.T) {
	fx := newAccessFixture(t)
	images := []any{}
	for i := 0; i < 12; i++ {
		images = append(images, map[string]any{"ref": strings.Repeat("r", 300), "id": "abc", "size": -1, "containers": -1})
	}
	dockerAgent(t, fx, "0.2.17", func(map[string]any) map[string]any {
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"outcome": "ok", "summary": map[string]any{
			"totals": dockerSummary["totals"], "images": images,
		}}}
	})
	code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	p := objectPayload(t, env)
	got, _ := p["images"].([]any)
	if len(got) != 10 {
		t.Fatalf("images %d", len(got))
	}
	first := got[0].(map[string]any)
	if len(first["ref"].(string)) != 200 || first["size"] != nil || first["containers"] != float64(0) {
		t.Fatalf("image %#v", first)
	}
	if v, ok := p["volumes"].([]any); !ok || len(v) != 0 {
		t.Fatalf("volumes %#v", p["volumes"])
	}
}

// TestDockerUsageRefusals: revoked 404, offline 503 (as every host_offline), old agent 409, each
// audited as denied; a dev build of 0.2.17 is let through.
func TestDockerUsageRefusals(t *testing.T) {
	t.Run("offline", func(t *testing.T) {
		fx := newAccessFixture(t)
		code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		mustHTTP(t, code, env, http.StatusServiceUnavailable, "host_offline", "host unavailable")
		rows := dockerAudits(fx.w)
		if len(rows) != 1 || rows[0].Decision != audit.DecisionDenied || rows[0].ErrorCode == nil || *rows[0].ErrorCode != "host_offline" {
			t.Fatalf("audit %+v", rows)
		}
	})
	t.Run("old agent", func(t *testing.T) {
		fx := newAccessFixture(t)
		tasks := dockerAgent(t, fx, "0.2.16", dockerOK)
		code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		mustHTTP(t, code, env, http.StatusConflict, "agent_too_old", "Docker usage needs bot-agent 0.2.17 or later on this host.")
		select {
		case task := <-tasks:
			t.Fatalf("task sent to an old agent %#v", task)
		default:
		}
		rows := dockerAudits(fx.w)
		if len(rows) != 1 || rows[0].ErrorCode == nil || *rows[0].ErrorCode != "agent_too_old" {
			t.Fatalf("audit %+v", rows)
		}
	})
	t.Run("dev build", func(t *testing.T) {
		fx := newAccessFixture(t)
		dockerAgent(t, fx, "0.2.17.1", dockerOK)
		code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	})
	t.Run("revoked", func(t *testing.T) {
		fx := newAccessFixture(t)
		code, env := fx.do(actOwnerOwner, http.MethodDelete, fx.hostPath("/credential"), "")
		mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
		code, env = fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		mustHTTP(t, code, env, http.StatusNotFound, "not_found", "not found")
	})
}

// TestDockerUsageFailures: each agent reason maps to its status and fixed
// message; agent text never reaches the caller.
func TestDockerUsageFailures(t *testing.T) {
	cases := []struct {
		name   string
		reply  map[string]any
		status int
		code   string
		msg    string
	}{
		{"disabled result", map[string]any{"type": "task_result", "state": "failed", "error_code": "docker_usage_disabled"},
			http.StatusConflict, "docker_usage_disabled", "Docker usage needs images or edge turned on in this host's runner.toml"},
		{"disabled outcome", map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"outcome": "failed", "reason": "docker_usage_disabled"}},
			http.StatusConflict, "docker_usage_disabled", "Docker usage needs images or edge turned on in this host's runner.toml"},
		{"unavailable", map[string]any{"type": "task_result", "state": "failed", "error_code": "docker_unavailable", "payload": map[string]any{"detail": "dial unix /var/run/docker.sock"}},
			http.StatusBadGateway, "docker_unavailable", "Docker is not reachable on the host"},
		{"failed payload reason", map[string]any{"type": "task_result", "state": "failed", "payload": map[string]any{"outcome": "failed", "reason": "timeout"}},
			http.StatusGatewayTimeout, "timeout", "The host did not answer in time"},
		{"timed out", map[string]any{"type": "task_result", "state": "timed_out"},
			http.StatusGatewayTimeout, "timeout", "The host did not answer in time"},
		{"agent busy", map[string]any{"type": "task_reject", "error_code": "busy"},
			http.StatusConflict, "busy", "A Docker usage check is already running on this host; try again shortly."},
		{"failed without a reason", map[string]any{"type": "task_result", "state": "failed"},
			http.StatusBadGateway, "bad_gateway", "The host could not report Docker usage"},
		{"unknown kind", map[string]any{"type": "task_reject", "error_code": "bad_spec"},
			http.StatusBadGateway, "bad_gateway", "The host could not report Docker usage"},
		{"free text", map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"outcome": "failed", "reason": "engine said: /secret/path"}},
			http.StatusBadGateway, "bad_gateway", "The host could not report Docker usage"},
		{"unreadable", map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"outcome": "ok", "summary": map[string]any{"images": []any{}}}},
			http.StatusBadGateway, "bad_gateway", "The host could not report Docker usage"},
		// Only v1.25's {"outcome":"ok","summary":…} is a success; a bare summary is not.
		{"bare summary", map[string]any{"type": "task_result", "state": "succeeded", "payload": map[string]any{"totals": map[string]any{}}},
			http.StatusBadGateway, "bad_gateway", "The host could not report Docker usage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newAccessFixture(t)
			dockerAgent(t, fx, "0.2.17", func(map[string]any) map[string]any {
				out := map[string]any{}
				for k, v := range tc.reply {
					out[k] = v
				}
				return out
			})
			code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
			mustHTTP(t, code, env, tc.status, tc.code, tc.msg)
			if env.Payload != nil {
				t.Fatalf("payload %#v", env.Payload)
			}
			rows := dockerAudits(fx.w)
			if len(rows) != 1 || rows[0].Decision != audit.DecisionAllowed || rows[0].ErrorCode == nil || *rows[0].ErrorCode != tc.code {
				t.Fatalf("audit %+v", rows)
			}
		})
	}
	t.Run("no answer", func(t *testing.T) {
		fx := newAccessFixture(t)
		fx.w.srv.dockerUsageWaitTest = 200 * time.Millisecond
		dockerAgent(t, fx, "0.2.17", func(map[string]any) map[string]any { return nil })
		code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		mustHTTP(t, code, env, http.StatusGatewayTimeout, "timeout", "The host did not answer in time")
	})
}

// TestDockerUsageOnePerHost (spec §8): a second call while one runs is 409
// busy and sends no task; the guard is released when the first ends.
func TestDockerUsageOnePerHost(t *testing.T) {
	fx := newAccessFixture(t)
	release := make(chan struct{})
	var answered atomic.Int32
	tasks := dockerAgent(t, fx, "0.2.17", func(map[string]any) map[string]any {
		if answered.Add(1) == 1 {
			<-release
		}
		return dockerOK(nil)
	})
	first := make(chan httpResult, 1)
	go func() {
		code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/docker-usage"), "")
		first <- httpResult{code, env}
	}()
	select {
	case <-tasks:
	case <-time.After(5 * time.Second):
		t.Fatal("no first task")
	}
	code, env := fx.do(actOwnerAdmin, http.MethodPost, fx.hostPath("/docker-usage"), "")
	mustHTTP(t, code, env, http.StatusConflict, "busy", "A Docker usage check is already running on this host; try again shortly.")
	close(release)
	got := <-first
	mustHTTP(t, got.status, got.env, http.StatusOK, "ok", "ok")
	// The guard is released: a new call runs.
	code, env = fx.do(actOwnerAdmin, http.MethodPost, fx.hostPath("/docker-usage"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	if n := answered.Load(); n != 2 {
		t.Fatalf("tasks answered %d, want 2 (none for the busy call)", n)
	}
	var busy int
	for _, a := range dockerAudits(fx.w) {
		if a.Decision == audit.DecisionDenied && a.ErrorCode != nil && *a.ErrorCode == "busy" {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("busy audit rows %d", busy)
	}
}

// TestDockerUsageAgentOld: before 0.2.17, empty, unparseable and 0.2.16
// dev builds are old; 0.2.17, its dev builds and later are not.
func TestDockerUsageAgentOld(t *testing.T) {
	for version, old := range map[string]bool{
		"": true, "abc": true, "0.2": true, "v0.2.17": true, "0.2.17-rc1": true,
		"0.2.16": true, "0.2.16.9": true, "0.1.99": true,
		"0.2.17": false, "0.2.17.1": false, "0.2.18": false, "0.3.0": false, "1.0.0": false,
	} {
		if got := dockerUsageAgentOld(version); got != old {
			t.Errorf("dockerUsageAgentOld(%q) = %v, want %v", version, got, old)
		}
	}
}
