package tools

import (
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.59 item 3: host_disk_usage reads /disks and posts docker-usage.

const duHost = "7a3a8a2e-4a59-4c6e-9d43-0d6f2b1f6c11"

const duDisks = `{"code":"ok","msg":"ok","payload":{"items":[` +
	`{"path":"/data","fstype":"xfs","total":4000000000,"used":1000000000,"used_percent":25,"at":"2026-11-24T10:00:00Z"},` +
	`{"path":"/","fstype":"ext4","total":21000000000,"used":12400000000,"used_percent":59.05,"at":"2026-11-24T10:00:00Z"}]}}`

const duDocker = `{"code":"ok","msg":"ok","payload":{"totals":{` +
	`"images":{"count":3,"active":1,"size":5000000000,"reclaimable":3000000000},` +
	`"containers":{"count":2,"active":1,"size":100000000,"reclaimable":100000000},` +
	`"volumes":{"count":1,"active":1,"size":1000000000,"reclaimable":0},` +
	`"build_cache":{"count":0,"active":0,"size":0,"reclaimable":100000000}},` +
	`"images":[],"volumes":[],"containers":[],"at":"2026-11-24T10:00:05Z"}}`

func duReply(docker func() (int, string)) func(*http.Request) (int, string) {
	return func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, duDisks
		}
		return docker()
	}
}

func TestHostDiskUsageSummary(t *testing.T) {
	cs, seen := vrSession(t, nil, duReply(func() (int, string) { return http.StatusOK, duDocker }))
	res, text, out := callVR(t, cs, "host_disk_usage", map[string]any{"host_id": duHost})
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	if text != "/ 59% (12.4 of 21.0 GB), /data 25% (1.0 of 4.0 GB); Docker 6.1 GB, 3.2 GB reclaimable" {
		t.Fatalf("summary %q", text)
	}
	disks, _ := out["disks"].([]any)
	docker, _ := out["docker"].(map[string]any)
	if len(disks) != 2 || docker["at"] != "2026-11-24T10:00:05Z" || docker["totals"] == nil {
		t.Fatalf("out %#v", out)
	}
	calls := seen()
	if len(calls) != 2 || calls[0].Method != http.MethodGet || calls[0].Path != "/v1/hosts/"+duHost+"/disks" ||
		calls[1].Method != http.MethodPost || calls[1].Path != "/v1/hosts/"+duHost+"/docker-usage" {
		t.Fatalf("calls %#v", calls)
	}
}

// A Docker refusal keeps the disks and carries the API message only.
func TestHostDiskUsageDockerError(t *testing.T) {
	cs, _ := vrSession(t, nil, duReply(func() (int, string) {
		return http.StatusForbidden, `{"code":"forbidden","msg":"forbidden","payload":null}`
	}))
	res, text, out := callVR(t, cs, "host_disk_usage", map[string]any{"host_id": duHost})
	if res.IsError {
		t.Fatalf("error %s", text)
	}
	docker, _ := out["docker"].(map[string]any)
	if len(docker) != 1 || docker["error"] != "forbidden" {
		t.Fatalf("docker %#v", out["docker"])
	}
	if disks, _ := out["disks"].([]any); len(disks) != 2 {
		t.Fatalf("disks %#v", out["disks"])
	}
	if !strings.HasSuffix(text, "; Docker: forbidden") {
		t.Fatalf("summary %q", text)
	}

	cs, _ = vrSession(t, nil, duReply(func() (int, string) {
		return http.StatusConflict, `{"code":"agent_too_old","msg":"Docker usage needs bot-agent 0.2.17 or later on this host.","payload":null}`
	}))
	_, _, out = callVR(t, cs, "host_disk_usage", map[string]any{"host_id": duHost})
	if docker, _ := out["docker"].(map[string]any); docker["error"] != "Docker usage needs bot-agent 0.2.17 or later on this host." {
		t.Fatalf("old agent %#v", out["docker"])
	}
}

// A refused disks read is the tool's error; no Docker call is made.
func TestHostDiskUsageDisksRefused(t *testing.T) {
	cs, seen := vrSession(t, nil, func(*http.Request) (int, string) {
		return http.StatusNotFound, `{"code":"not_found","msg":"not found","payload":null}`
	})
	res, text, _ := callVR(t, cs, "host_disk_usage", map[string]any{"host_id": duHost})
	if !res.IsError || text != "not_found: not found" || len(seen()) != 1 {
		t.Fatalf("refused %v %q %d", res.IsError, text, len(seen()))
	}
}

func TestSummarizeDiskUsageEmpty(t *testing.T) {
	if got := summarizeDiskUsage([]byte(`{"disks":[],"docker":{"error":"host unavailable"}}`)); got != "No disk data; Docker: host unavailable" {
		t.Fatalf("summary %q", got)
	}
}

func TestDecimalBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1000: "1.0 kB", 999_999: "1.0 MB", 800_000_000: "800.0 MB",
		6_100_000_000: "6.1 GB", 999_960_000_000: "1.0 TB",
	} {
		if got := decimalBytes(n); got != want {
			t.Errorf("decimalBytes(%d) = %q, want %q", n, got, want)
		}
	}
	if got := usedOfTotal(800_000_000, 21_000_000_000); got != "800.0 MB of 21.0 GB" {
		t.Errorf("mixed units %q", got)
	}
	if got := usedOfTotal(12_400_000_000, 21_000_000_000); got != "12.4 of 21.0 GB" {
		t.Errorf("same unit %q", got)
	}
	got := summarizeDiskUsage([]byte(`{"disks":[],"docker":{"totals":{"images":{"size":500000000,"reclaimable":0}}}}`))
	if got != "No disk data; Docker 500.0 MB, 0 B reclaimable" {
		t.Errorf("small docker %q", got)
	}
}
