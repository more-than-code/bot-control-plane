package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// api-v1 v1.49 item 6: get_alert_settings, set_alert_settings through
// confirm, query_metrics path, and the list_problems text.

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
	"disk_max_used_percent": nil, "cpu_max_busy_percent": nil, "idempotency_key": "as-1",
}

func TestAlertSettingsTools(t *testing.T) {
	cs, seen := vrSession(t, nil, alertReply)
	res, text, out := callVR(t, cs, "get_alert_settings", map[string]any{"host_id": asHost})
	if res.IsError || text != "Alerts on: memory available under 15 % (default), disk at 95 %, CPU at 90 % (default)." || out["disk_max_used_percent"] != float64(95) {
		t.Fatalf("get %v %q %v", res.IsError, text, out)
	}
	// set: a plan kept by this server; the API only gets a host read.
	before := len(seen())
	res, text, out = callVR(t, cs, "set_alert_settings", asSet)
	id, _ := out["confirmation_id"].(string)
	if res.IsError || len(id) != 43 || !strings.Contains(text, "Set resource alerts on pi1: memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)") {
		t.Fatalf("set %v %q %v", res.IsError, text, out)
	}
	calls := seen()[before:]
	if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/hosts/"+asHost {
		t.Fatalf("set calls %#v", calls)
	}
	// A wrong key keeps the plan and sends nothing.
	before = len(seen())
	if res, text, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "other"}); !res.IsError || !strings.Contains(text, "idempotency_key does not match") {
		t.Fatalf("wrong key %q", text)
	}
	if len(seen()) != before {
		t.Fatal("wrong key reached the API")
	}
	// confirm sends the PUT with the four fields only.
	res, text, out = callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "as-1"})
	if res.IsError || text != "Alerts on: memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)." || out["mem_min_available_percent"] != float64(10) {
		t.Fatalf("confirm %v %q %v", res.IsError, text, out)
	}
	calls = seen()
	put := calls[len(calls)-1]
	var body map[string]any
	if err := json.Unmarshal([]byte(put.Body), &body); err != nil || put.Method != http.MethodPut || put.Path != "/v1/hosts/"+asHost+"/alert-settings" ||
		body["mem_min_available_percent"] != float64(10) || body["alerts_enabled"] != true || body["disk_max_used_percent"] != nil || len(body) != 4 {
		t.Fatalf("put %#v", put)
	}
	// Used once: a second confirm is not this server's plan any more and goes to the API.
	res, _, _ = callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "as-1"})
	if !res.IsError {
		t.Fatal("second confirm succeeded")
	}
	if last := seen()[len(seen())-1]; last.Method != http.MethodPost || !strings.HasPrefix(last.Path, "/v1/confirmations/") {
		t.Fatalf("second confirm %#v", last)
	}
	// Out of range and missing fields are refused before the API.
	before = len(seen())
	for _, args := range []map[string]any{
		{"host_id": asHost, "alerts_enabled": true, "mem_min_available_percent": 4, "idempotency_key": "as-2"},
		{"host_id": asHost, "alerts_enabled": true, "cpu_max_busy_percent": 101, "idempotency_key": "as-2"},
		{"host_id": asHost, "mem_min_available_percent": 10, "idempotency_key": "as-2"},
		{"host_id": asHost, "alerts_enabled": true},
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

// TestAlertSettingsPlanCaller: another caller cannot confirm the plan (it
// falls through to the API, which does not know the id).
func TestAlertSettingsPlanCaller(t *testing.T) {
	owner, ownerSeen := vrSessionCaller(t, "", "owner-token", nil, alertReply)
	_, _, out := callVR(t, owner, "set_alert_settings", asSet)
	id, _ := out["confirmation_id"].(string)
	other, otherSeen := vrSessionCaller(t, "", "other-token", nil, alertReply)
	if res, _, _ := callVR(t, other, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "as-1"}); !res.IsError {
		t.Fatal("another caller confirmed")
	}
	for _, c := range otherSeen() {
		if c.Method == http.MethodPut {
			t.Fatal("another caller's confirm sent the PUT")
		}
	}
	if res, _, _ := callVR(t, owner, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "as-1"}); res.IsError {
		t.Fatal("owner confirm after the other caller failed")
	}
	if last := ownerSeen()[len(ownerSeen())-1]; last.Method != http.MethodPut {
		t.Fatalf("owner confirm %#v", last)
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

// TestAlertSettingsOffAndFailures (review items 12 and 14): alerts off keeps
// the thresholds in the plan and the read summary; a refused or unexpected
// PUT at confirm is an error, and the plan is used up.
func TestAlertSettingsOffAndFailures(t *testing.T) {
	off := `{"code":"ok","msg":"ok","payload":{"alerts_enabled":false,"mem_min_available_percent":10,"disk_max_used_percent":null,"cpu_max_busy_percent":null}}`
	var putStatusCode atomic.Int32
	putStatusCode.Store(http.StatusOK)
	reply := func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/hosts/"+asHost:
			return http.StatusOK, `{"code":"ok","msg":"ok","payload":{"id":"` + asHost + `","name":"pi1"}}`
		case r.Method == http.MethodGet:
			return http.StatusOK, off
		case r.Method == http.MethodPut:
			switch putStatusCode.Load() {
			case http.StatusForbidden:
				return http.StatusForbidden, `{"code":"forbidden","msg":"forbidden","payload":null}`
			case http.StatusAccepted:
				return http.StatusAccepted, `{"code":"ok","msg":"ok","payload":{"confirmation_id":"x"}}`
			}
			return http.StatusOK, off
		}
		return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
	}
	cs, _ := vrSession(t, nil, reply)
	if _, text, _ := callVR(t, cs, "get_alert_settings", map[string]any{"host_id": asHost}); text != "Alerts off for this host; thresholds kept: memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)." {
		t.Fatalf("off summary %q", text)
	}
	args := map[string]any{"host_id": asHost, "alerts_enabled": false, "mem_min_available_percent": 10, "idempotency_key": "off-1"}
	_, text, out := callVR(t, cs, "set_alert_settings", args)
	if !strings.Contains(text, "Turn resource alerts off on pi1; thresholds kept: memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)") {
		t.Fatalf("off plan %q", text)
	}
	for _, code := range []int32{http.StatusForbidden, http.StatusAccepted} {
		putStatusCode.Store(code)
		_, _, out = callVR(t, cs, "set_alert_settings", args)
		id, _ := out["confirmation_id"].(string)
		res, text, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "off-1"})
		if !res.IsError {
			t.Fatalf("status %d: confirm succeeded: %q", code, text)
		}
		if code == http.StatusForbidden && !strings.Contains(text, "forbidden") {
			t.Fatalf("403 text %q", text)
		}
		if code == http.StatusAccepted && !strings.Contains(text, "alert settings response was not usable") {
			t.Fatalf("202 text %q", text)
		}
		// Used once: the same id now goes to the API, which does not know it.
		if res, _, _ := callVR(t, cs, "confirm", map[string]any{"confirmation_id": id, "idempotency_key": "off-1"}); !res.IsError {
			t.Fatal("plan reused")
		}
	}
}
