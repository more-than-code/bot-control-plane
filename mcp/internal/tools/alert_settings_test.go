package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// api-v1 v1.49 item 6 and v1.60: get_alert_settings, set_alert_settings as
// a console link (no confirm), query_metrics path, and the list_problems
// text.

const asHost = "22222222-2222-4222-8222-222222222222"

func alertReply(r *http.Request) (int, string) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/hosts/"+asHost:
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"id":"` + asHost + `","name":"pi1"}}`
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/alert-settings"):
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"alerts_enabled":true,"mem_min_available_percent":null,"disk_max_used_percent":95,"cpu_max_busy_percent":null}}`
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/alert-settings"):
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"alerts_enabled":true,"mem_min_available_percent":10,"disk_max_used_percent":null,"cpu_max_busy_percent":null}}`
	case strings.HasSuffix(r.URL.Path, "/metrics"):
		metricsQuery.Store(r.URL.RawQuery)
		return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"m":"disk","series":[{"name":"used_percent","unit":"%","points":[]}]}}`
	}
	return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
}

// metricsQuery is the raw query of the latest /metrics call.
var metricsQuery atomic.Value

var asSet = map[string]any{
	"host_id": asHost, "alerts_enabled": true, "mem_min_available_percent": 10,
	"disk_max_used_percent": nil, "cpu_max_busy_percent": nil,
}

func TestAlertSettingsTools(t *testing.T) {
	cs, seen := vrSession(t, nil, alertReply)
	res, text, out := callVR(t, cs, "get_alert_settings", map[string]any{"host_id": asHost})
	if res.IsError || text != "Alerts on: memory available under 15 % (default), disk at 95 %, CPU at 90 % (default)." || out["disk_max_used_percent"] != float64(95) {
		t.Fatalf("get %v %q %v", res.IsError, text, out)
	}
	// set: reads the host and its settings, proposes, never writes, and
	// gives no confirmation_id. Without a console origin there is no link.
	before := len(seen())
	res, text, out = callVR(t, cs, "set_alert_settings", asSet)
	want := "Proposed for pi1: memory available under 15 % (default) → 10 %; disk at 95 % → 90 % (default). Nothing has changed: ask the person to open this host's Alerts card in the console and make the change there."
	if res.IsError || text != want || out["confirmation_id"] != nil || out["console_url"] != "" || out["host"] != "pi1" {
		t.Fatalf("set %v %q %v", res.IsError, text, out)
	}
	if ch, _ := out["changes"].([]any); len(ch) != 2 {
		t.Fatalf("changes %v", out["changes"])
	}
	calls := seen()[before:]
	if len(calls) != 2 || calls[0].Path != "/v1/hosts/"+asHost || calls[1].Path != "/v1/hosts/"+asHost+"/alert-settings" {
		t.Fatalf("set calls %#v", calls)
	}
	for _, c := range calls {
		if c.Method != http.MethodGet {
			t.Fatalf("set sent %s %s", c.Method, c.Path)
		}
	}
	// An agent that still sends idempotency_key is not refused.
	withKey := map[string]any{"idempotency_key": "as-1"}
	for k, v := range asSet {
		withKey[k] = v
	}
	if res, _, _ := callVR(t, cs, "set_alert_settings", withKey); res.IsError {
		t.Fatal("idempotency_key refused")
	}
	// The same settings: nothing to change, no link.
	same := map[string]any{"host_id": asHost, "alerts_enabled": true, "disk_max_used_percent": 95}
	res, text, out = callVR(t, cs, "set_alert_settings", same)
	if res.IsError || !strings.HasPrefix(text, "Nothing to change on pi1") || out["console_url"] != nil {
		t.Fatalf("same %v %q %v", res.IsError, text, out)
	}
	// Out of range and missing fields are refused before the API.
	before = len(seen())
	for _, args := range []map[string]any{
		{"host_id": asHost, "alerts_enabled": true, "mem_min_available_percent": 4},
		{"host_id": asHost, "alerts_enabled": true, "cpu_max_busy_percent": 101},
		{"host_id": asHost, "mem_min_available_percent": 10},
		{"host_id": "not-a-uuid", "alerts_enabled": true},
	} {
		if res, _, _ := callVR(t, cs, "set_alert_settings", args); !res.IsError {
			t.Fatalf("accepted %v", args)
		}
	}
	if len(seen()) != before {
		t.Fatal("a refused set reached the API")
	}
	// query_metrics path: disk only.
	res, _, _ = callVR(t, cs, "query_metrics", map[string]any{"host_id": asHost, "metric": "disk", "path": "/data"})
	if res.IsError {
		t.Fatal("disk path refused")
	}
	calls = seen()
	if last := calls[len(calls)-1]; last.Path != "/v1/hosts/"+asHost+"/metrics" {
		t.Fatalf("metrics %#v", last)
	}
	if q, _ := metricsQuery.Load().(string); q != "m=disk&path=%2Fdata" {
		t.Fatalf("metrics query %q", q)
	}
	if res, _, _ := callVR(t, cs, "query_metrics", map[string]any{"host_id": asHost, "metric": "cpu", "path": "/data"}); !res.IsError {
		t.Fatal("path accepted for cpu")
	}
}

// TestLocalPlans: 10 pending per caller, a 10-minute TTL, used once.
func TestLocalPlans(t *testing.T) {
	store := newLocalPlans[int](10*time.Minute, "test ")
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	a, b := [32]byte{1}, [32]byte{2}
	var ids []string
	for i := range maxLocalPlansPerCaller {
		id, exp, err := store.add(a, "k", i)
		if err != nil || len(id) != 43 || !exp.Equal(now.Add(10*time.Minute)) {
			t.Fatalf("add %d: %q %v %v", i, id, exp, err)
		}
		ids = append(ids, id)
	}
	if _, _, err := store.add(a, "k", 99); err == nil {
		t.Fatal("11th plan for one caller accepted")
	}
	if _, _, err := store.add(b, "k", 1); err != nil {
		t.Fatalf("other caller refused: %v", err)
	}
	if v, found, fail := store.take(a, ids[3], "k"); !found || fail != nil || v != 3 {
		t.Fatalf("take %v %v %v", v, found, fail)
	}
	if _, found, _ := store.take(a, ids[3], "k"); found {
		t.Fatal("used twice")
	}
	now = now.Add(10 * time.Minute)
	if _, found, fail := store.take(a, ids[4], "k"); !found || fail == nil {
		t.Fatal("expired plan accepted")
	}
	// Expired plans no longer count toward the cap.
	if _, _, err := store.add(a, "k", 5); err != nil {
		t.Fatalf("add after expiry: %v", err)
	}
}

func TestSummarizeHostResourceProblem(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"kind": "host_resource", "level": "needs_action", "title": "pi1 is low on memory", "opened_at": "2026-11-23T08:00:00Z", "resolved_at": nil},
	}})
	if text := summarizeProblems(payload); !strings.Contains(text, "pi1 is low on memory since 2026-11-23 08:00 UTC (needs_action) — free memory or disk on the host, or raise its threshold") {
		t.Fatalf("summary %q", text)
	}
}

// TestAlertSettingsOff: alerts off keeps the thresholds in the read
// summary; turning them off is a proposal like any other, and a refused
// settings read is an error with no link.
func TestAlertSettingsOff(t *testing.T) {
	off := `{"code":"ok","msg":"ok","payload":{"alerts_enabled":false,"mem_min_available_percent":10,"disk_max_used_percent":null,"cpu_max_busy_percent":null}}`
	var settingsStatus atomic.Int32
	settingsStatus.Store(http.StatusOK)
	reply := func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/hosts/"+asHost:
			return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"id":"` + asHost + `","name":"pi1"}}`
		case r.Method == http.MethodGet && settingsStatus.Load() == http.StatusForbidden:
			return http.StatusForbidden, `{"code":"forbidden","msg":"forbidden","payload":null}`
		case r.Method == http.MethodGet:
			return http.StatusOK, off
		}
		return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
	}
	cs, seen := vrSession(t, nil, reply)
	if _, text, _ := callVR(t, cs, "get_alert_settings", map[string]any{"host_id": asHost}); text != "Alerts off for this host; thresholds kept: memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)." {
		t.Fatalf("off summary %q", text)
	}
	_, text, _ := callVR(t, cs, "set_alert_settings", map[string]any{"host_id": asHost, "alerts_enabled": true, "mem_min_available_percent": 10})
	if !strings.HasPrefix(text, "Proposed for pi1: alerts off → on.") {
		t.Fatalf("on plan %q", text)
	}
	settingsStatus.Store(http.StatusForbidden)
	if res, text, out := callVR(t, cs, "set_alert_settings", map[string]any{"host_id": asHost, "alerts_enabled": true}); !res.IsError || out["console_url"] != nil {
		t.Fatalf("refused read %v %q %v", res.IsError, text, out)
	}
	for _, c := range seen() {
		if c.Method != http.MethodGet {
			t.Fatalf("sent %s %s", c.Method, c.Path)
		}
	}
	// An alert-settings confirmation id is not this server's plan any more:
	// confirm asks the API.
	before := len(seen())
	if res, _, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": strings.Repeat("a", 43), "idempotency_key": "as-1"}); !res.IsError {
		t.Fatal("unknown confirmation succeeded")
	}
	if calls := seen()[before:]; len(calls) != 1 || !strings.HasPrefix(calls[0].Path, "/v1/confirmations/") {
		t.Fatalf("confirm calls %#v", calls)
	}
}

// TestHostAlertsURL: only the fields that change, null as default, and no
// link without an origin, a host id or a change.
func TestHostAlertsURL(t *testing.T) {
	api := linkClient(t, "https://console.example/")
	n := func(v int) *int { return &v }
	cur := alertSettings{AlertsEnabled: true, DiskMaxUsedPercent: n(95)}
	for _, tc := range []struct {
		to   alertSettings
		want string
	}{
		{alertSettings{AlertsEnabled: false, DiskMaxUsedPercent: n(95)}, "?alerts.enabled=off#alerts"},
		{alertSettings{AlertsEnabled: true, DiskMaxUsedPercent: n(99)}, "?alerts.disk=99#alerts"},
		{alertSettings{AlertsEnabled: true}, "?alerts.disk=default#alerts"},
		{alertSettings{AlertsEnabled: false, MemMinAvailablePercent: n(5), DiskMaxUsedPercent: n(95), CPUMaxBusyPercent: n(100)}, "?alerts.enabled=off&alerts.mem=5&alerts.cpu=100#alerts"},
		{cur, ""},
	} {
		want := tc.want
		if want != "" {
			want = "https://console.example/hosts/" + lnHost + want
		}
		if got := hostAlertsURL(api, lnHost, cur, tc.to); got != want {
			t.Errorf("%+v: %q, want %q", tc.to, got, want)
		}
	}
	off := alertSettings{AlertsEnabled: false}
	if got := hostAlertsURL(api, "not-a-uuid", cur, off); got != "" {
		t.Fatalf("bad id %q", got)
	}
	if got := hostAlertsURL(linkClient(t, ""), lnHost, cur, off); got != "" {
		t.Fatalf("no origin %q", got)
	}
}
