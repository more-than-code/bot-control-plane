package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.49 items 3 and 4: alert settings, mounts and the metrics
// additions.

func alertRows(w *world) []store.AuditEntry {
	var out []store.AuditEntry
	for _, row := range w.mem.AllAudits() {
		if row.Action == audit.ActionHostAlertSettings {
			out = append(out, row)
		}
	}
	return out
}

// TestAlertSettingsRoles is §8 routes: any host viewer reads; org owners
// and admins write; project roles get forbidden; no relation is not_found.
func TestAlertSettingsRoles(t *testing.T) {
	read := map[string]int{
		actOwnerOwner: http.StatusOK, actOwnerAdmin: http.StatusOK, actOwnerViewer: http.StatusOK,
		actMetricsViewer: http.StatusOK, actMetricsAdmin: http.StatusOK, actDeployViewer: http.StatusOK, actDeployAdmin: http.StatusOK,
		actOrgMember: http.StatusNotFound, actNone: http.StatusNotFound,
	}
	write := map[string]int{
		actOwnerOwner: http.StatusOK, actOwnerAdmin: http.StatusOK, actOwnerViewer: http.StatusForbidden,
		actMetricsViewer: http.StatusForbidden, actMetricsAdmin: http.StatusForbidden, actDeployViewer: http.StatusForbidden, actDeployAdmin: http.StatusForbidden,
		actOrgMember: http.StatusNotFound, actNone: http.StatusNotFound,
	}
	for _, actor := range accessActors {
		fx := newAccessFixture(t)
		code, env := fx.do(actor, http.MethodGet, fx.hostPath("/alert-settings"), "")
		if code != read[actor] {
			t.Fatalf("%s read: %d %s", actor, code, env.Msg)
		}
		if code == http.StatusOK {
			got := objectPayload(t, env)
			if got["alerts_enabled"] != true || got["mem_min_available_percent"] != nil || got["disk_max_used_percent"] != nil || got["cpu_max_busy_percent"] != nil || len(got) != 4 {
				t.Fatalf("%s defaults %v", actor, got)
			}
		}
		code, env = fx.do(actor, http.MethodPut, fx.hostPath("/alert-settings"), `{"alerts_enabled":true,"mem_min_available_percent":10,"disk_max_used_percent":null,"cpu_max_busy_percent":80}`)
		if code != write[actor] {
			t.Fatalf("%s write: %d %s", actor, code, env.Msg)
		}
		stored, _ := fx.w.mem.GetHostAlertSettings(context.Background(), fx.sc.host.ID)
		if code != http.StatusOK {
			if stored.MemMinAvailablePercent != nil {
				t.Fatalf("%s: refused write stored %+v", actor, stored)
			}
			continue
		}
		got := objectPayload(t, env)
		if got["mem_min_available_percent"] != float64(10) || got["cpu_max_busy_percent"] != float64(80) || got["disk_max_used_percent"] != nil {
			t.Fatalf("%s payload %v", actor, got)
		}
		if stored.MemMinAvailablePercent == nil || *stored.MemMinAvailablePercent != 10 {
			t.Fatalf("%s stored %+v", actor, stored)
		}
	}
}

// TestAlertSettingsValidationAndAudit: ranges, the strict body, a revoked
// host, and one audit row per change naming the changed fields only.
func TestAlertSettingsValidationAndAudit(t *testing.T) {
	fx := newAccessFixture(t)
	path := fx.hostPath("/alert-settings")
	for body, msg := range map[string]string{
		`{"alerts_enabled":true,"mem_min_available_percent":4}`:  "mem_min_available_percent must be 5–50 or null",
		`{"alerts_enabled":true,"mem_min_available_percent":51}`: "mem_min_available_percent must be 5–50 or null",
		`{"alerts_enabled":true,"disk_max_used_percent":49}`:     "disk_max_used_percent must be 50–99 or null",
		`{"alerts_enabled":true,"disk_max_used_percent":100}`:    "disk_max_used_percent must be 50–99 or null",
		`{"alerts_enabled":true,"cpu_max_busy_percent":49}`:      "cpu_max_busy_percent must be 50–100 or null",
		`{"alerts_enabled":true,"cpu_max_busy_percent":101}`:     "cpu_max_busy_percent must be 50–100 or null",
		`{"mem_min_available_percent":10}`:                       "alerts_enabled is required",
		`{"alerts_enabled":true,"cpu_max_busy_percent":80.5}`:    "invalid json",
		`{"alerts_enabled":true,"swap_max":1}`:                   "invalid json",
		`{"alerts_enabled":"yes"}`:                               "invalid json",
	} {
		code, env := fx.do(actOwnerOwner, http.MethodPut, path, body)
		mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", msg)
	}
	if n := len(alertRows(fx.w)); n != 0 {
		t.Fatalf("audit rows after refusals %d", n)
	}
	// Bounds are accepted.
	code, env := fx.do(actOwnerOwner, http.MethodPut, path, `{"alerts_enabled":false,"mem_min_available_percent":50,"disk_max_used_percent":99,"cpu_max_busy_percent":100}`)
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	// The same body again: nothing changes, nothing is audited.
	code, env = fx.do(actOwnerAdmin, http.MethodPut, path, `{"alerts_enabled":false,"mem_min_available_percent":50,"disk_max_used_percent":99,"cpu_max_busy_percent":100}`)
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	// Back to defaults for two fields.
	code, env = fx.do(actOwnerAdmin, http.MethodPut, path, `{"alerts_enabled":false,"mem_min_available_percent":5,"disk_max_used_percent":null,"cpu_max_busy_percent":null}`)
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	rows := alertRows(fx.w)
	if len(rows) != 2 {
		t.Fatalf("audit rows %d", len(rows))
	}
	last := rows[len(rows)-1]
	raw, _ := json.Marshal(last.Detail)
	var detail map[string]any
	_ = json.Unmarshal(raw, &detail)
	if detail["host"] != fx.sc.host.Name || detail["mem_min_available_percent"] != float64(5) || detail["disk_max_used_percent"] != nil ||
		len(detail) != 4 || last.Via != audit.ViaConsole || last.HostID == nil || *last.HostID != fx.sc.host.ID {
		t.Fatalf("audit %+v %s", last, raw)
	}
	if _, ok := detail["alerts_enabled"]; ok {
		t.Fatalf("unchanged field audited: %s", raw)
	}
	// A denied write is audited as denied.
	code, env = fx.do(actDeployAdmin, http.MethodPut, path, `{"alerts_enabled":true}`)
	mustHTTP(t, code, env, http.StatusForbidden, "forbidden", "forbidden")
	denied := 0
	for _, row := range alertRows(fx.w) {
		if row.Decision == audit.DecisionDenied {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("denied rows %d", denied)
	}
	// A revoked host refuses the write.
	if err := fx.w.mem.RevokeHost(context.Background(), fx.sc.host.ID, fx.w.now); err != nil {
		t.Fatal(err)
	}
	code, env = fx.do(actOwnerOwner, http.MethodPut, path, `{"alerts_enabled":true}`)
	mustHTTP(t, code, env, http.StatusConflict, "conflict", "the host is revoked")
}

// TestAlertSettingsDelegated: the API keeps no plan for alert settings (the
// MCP server holds the confirm step), so an MCP-delegated org owner's PUT
// stores at once and is audited via mcp; a delegated project admin is
// refused; delegated reads are allowed; an idempotency_key is not part of
// the body.
func TestAlertSettingsDelegated(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	path := fx.hostPath("/alert-settings")
	tok := tokenKind(t, w, fx.users[actOwnerOwner].ID, store.KindDelegated)
	code, env := w.do(tok, http.MethodPut, path, `{"alerts_enabled":true,"disk_max_used_percent":95,"idempotency_key":"k"}`)
	mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", "invalid json")
	code, env = w.do(tok, http.MethodPut, path, `{"alerts_enabled":true,"mem_min_available_percent":null,"disk_max_used_percent":95,"cpu_max_busy_percent":null}`)
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	if p := objectPayload(t, env); p["disk_max_used_percent"] != float64(95) || p["alerts_enabled"] != true {
		t.Fatalf("payload %v", p)
	}
	if s, _ := w.mem.GetHostAlertSettings(context.Background(), fx.sc.host.ID); s.DiskMaxUsedPercent == nil || *s.DiskMaxUsedPercent != 95 {
		t.Fatalf("stored %+v", s)
	}
	rows := alertRows(w)
	if len(rows) != 1 || rows[0].Via != audit.ViaMCP || rows[0].Decision != audit.DecisionAllowed {
		t.Fatalf("audit %+v", rows)
	}
	deployAdmin := tokenKind(t, w, fx.users[actDeployAdmin].ID, store.KindDelegated)
	code, env = w.do(deployAdmin, http.MethodPut, path, `{"alerts_enabled":false}`)
	mustHTTP(t, code, env, http.StatusForbidden, "forbidden", "forbidden")
	if s, _ := w.mem.GetHostAlertSettings(context.Background(), fx.sc.host.ID); !s.AlertsEnabled {
		t.Fatal("refused delegated write stored")
	}
	assertNoDelegatedDenied(t, w)
	code, env = w.do(tok, http.MethodGet, path, "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	code, env = w.do(tok, http.MethodGet, fx.hostPath("/disks"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
}

// TestHostDisksAndMetricsPath: the mounts of the last 24 hours, the disk
// chart per mount (default /, unknown empty), and mem available.
func TestHostDisksAndMetricsPath(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	host := fx.sc.host
	at := w.now.Add(-time.Minute)
	samples := []store.Sample{
		{M: "disk", T: at, Tags: map[string]string{"path": "/", "fstype": "ext4"}, Floats: map[string]float64{"used_percent": 50}},
		{M: "disk", T: at, Tags: map[string]string{"path": "/data", "fstype": "xfs"}, Floats: map[string]float64{"used_percent": 70}},
		{M: "disk", T: w.now.Add(-25 * time.Hour), Tags: map[string]string{"path": "/old", "fstype": "ext4"}, Floats: map[string]float64{"used_percent": 1}},
		{M: "mem", T: at, Ints: map[string]int64{"total": 1000, "available": 400, "used": 600}, Floats: map[string]float64{"used_percent": 60}},
	}
	if _, err := w.mem.InsertBatch(context.Background(), host.ID, uuid.New(), samples, w.now); err != nil {
		t.Fatal(err)
	}
	code, env := fx.do(actMetricsViewer, http.MethodGet, fx.hostPath("/disks"), "")
	mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
	raw, _ := json.Marshal(env.Payload)
	if string(raw) != `{"items":[{"fstype":"ext4","path":"/"},{"fstype":"xfs","path":"/data"}]}` {
		t.Fatalf("disks %s", raw)
	}
	code, _ = fx.do(actNone, http.MethodGet, fx.hostPath("/disks"), "")
	if code != http.StatusNotFound {
		t.Fatalf("outsider disks %d", code)
	}
	value := func(q string) (string, float64) {
		t.Helper()
		code, env := fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/metrics?"+q), "")
		mustHTTP(t, code, env, http.StatusOK, "ok", "ok")
		var body struct {
			Series []struct {
				Name   string   `json:"name"`
				Points [][2]any `json:"points"`
			} `json:"series"`
		}
		raw, _ := json.Marshal(env.Payload)
		_ = json.Unmarshal(raw, &body)
		last := body.Series[len(body.Series)-1]
		for _, p := range last.Points {
			if v, ok := p[1].(float64); ok {
				return last.Name, v
			}
		}
		return last.Name, -1
	}
	if name, v := value("m=disk"); name != "used_percent" || v != 50 {
		t.Fatalf("default mount %s %v", name, v)
	}
	if _, v := value("m=disk&path=%2Fdata"); v != 70 {
		t.Fatalf("/data %v", v)
	}
	if _, v := value("m=disk&path=%2Fnope"); v != -1 {
		t.Fatalf("unknown mount %v", v)
	}
	if name, v := value("m=mem"); name != "available" || v != 400 {
		t.Fatalf("mem available %s %v", name, v)
	}
	code, env = fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/metrics?m=disk&path="+strings.Repeat("a", 1025)), "")
	mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", "invalid path")
	code, env = fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/metrics?m=disk&path=%2Fa%07b"), "")
	mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", "invalid path")
}
