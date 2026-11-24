package agentws

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const usageTotals = `"totals":{"images":{"count":1,"active":1,"size":10,"reclaimable":0},` +
	`"containers":{"count":1,"active":1,"size":1,"reclaimable":0},` +
	`"volumes":{"count":1,"active":1,"size":1,"reclaimable":0},` +
	`"build_cache":{"count":0,"active":0,"size":0,"reclaimable":0}}`

func usageOK(t *testing.T, summary string) DockerUsageSummary {
	t.Helper()
	got, err := ParseDockerUsage(json.RawMessage(`{"outcome":"ok","summary":{` + summary + `}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return got
}

// A null entry and an entry with a wrong-typed field are dropped; the
// other entries stay.
func TestParseDockerUsageDropsBadEntries(t *testing.T) {
	got := usageOK(t, usageTotals+`,"images":[null,{"ref":"a:1","id":"aaa","size":"big","containers":0},{"ref":"b:1","id":"bbb","size":5,"containers":1}],`+
		`"volumes":[null,{"name":"v","size":3,"in_use":"yes"},{"name":"w","size":3,"in_use":false}],`+
		`"containers":[{"name":"c","state":1,"size":1},null,{"name":"d","state":"exited","size":null}]`)
	if len(got.Images) != 1 || got.Images[0].Ref != "b:1" {
		t.Fatalf("images %+v", got.Images)
	}
	if len(got.Volumes) != 1 || got.Volumes[0].Name != "w" {
		t.Fatalf("volumes %+v", got.Volumes)
	}
	if len(got.Containers) != 1 || got.Containers[0].Name != "d" || got.Containers[0].Size != nil {
		t.Fatalf("containers %+v", got.Containers)
	}
}

// Negative totals read as 0; a wrong-typed total is a bad answer.
func TestParseDockerUsageTotals(t *testing.T) {
	neg := strings.Replace(usageTotals, `"images":{"count":1,"active":1,"size":10,"reclaimable":0}`, `"images":{"count":-3,"active":-1,"size":-10,"reclaimable":-2}`, 1)
	got := usageOK(t, neg)
	if im := got.Totals.Images; im != (DockerUsageTotal{}) {
		t.Fatalf("negative totals %+v", im)
	}
	if got.Images == nil || got.Volumes == nil || got.Containers == nil {
		t.Fatalf("lists must not be null: %+v", got)
	}
	bad := strings.Replace(usageTotals, `"size":10`, `"size":"10"`, 1)
	_, err := ParseDockerUsage(json.RawMessage(`{"outcome":"ok","summary":{` + bad + `}}`))
	var ce *CallError
	if !errors.As(err, &ce) || ce.Agent != DockerUsageBadAnswer {
		t.Fatalf("wrong-typed total: %v", err)
	}
	_, err = ParseDockerUsage(json.RawMessage(`{"outcome":"ok","summary":{"images":[]}}`))
	if !errors.As(err, &ce) || ce.Agent != DockerUsageBadAnswer {
		t.Fatalf("no totals: %v", err)
	}
}

// Lists are sorted again by size (largest first, unknown last), then by
// name (images by ref, then id), and capped at ten after sorting.
func TestParseDockerUsageSortAndCap(t *testing.T) {
	var images []string
	for i := 0; i < 12; i++ {
		images = append(images, fmt.Sprintf(`{"ref":"r%02d","id":"x","size":%d,"containers":0}`, i, i))
	}
	images = append(images,
		`{"ref":"big","id":"b2","size":100,"containers":0}`,
		`{"ref":"big","id":"b1","size":100,"containers":0}`,
		`{"ref":"unknown","id":"u","size":null,"containers":0}`)
	got := usageOK(t, usageTotals+`,"images":[`+strings.Join(images, ",")+`],`+
		`"volumes":[{"name":"z","size":null,"in_use":false},{"name":"b","size":7,"in_use":true},{"name":"a","size":7,"in_use":false}],"containers":[]`)
	if len(got.Images) != 10 {
		t.Fatalf("images %d", len(got.Images))
	}
	if got.Images[0].ID != "b1" || got.Images[1].ID != "b2" || got.Images[2].Ref != "r11" || got.Images[9].Ref != "r04" {
		t.Fatalf("image order %+v", got.Images)
	}
	if v := got.Volumes; len(v) != 3 || v[0].Name != "a" || v[1].Name != "b" || v[2].Name != "z" {
		t.Fatalf("volume order %+v", v)
	}
}

// Only {"outcome":"ok","summary":…} is a success; a failed outcome keeps a
// short reason code and turns free text into "error".
func TestParseDockerUsageOutcome(t *testing.T) {
	var ce *CallError
	_, err := ParseDockerUsage(json.RawMessage(`{"outcome":"failed","reason":"docker_unavailable"}`))
	if !errors.As(err, &ce) || ce.Agent != DockerUsageUnavailable {
		t.Fatalf("failed: %v", err)
	}
	_, err = ParseDockerUsage(json.RawMessage(`{"outcome":"failed","reason":"engine said /var/lib"}`))
	if !errors.As(err, &ce) || ce.Agent != "error" {
		t.Fatalf("free text: %v", err)
	}
	_, err = ParseDockerUsage(json.RawMessage(`{` + usageTotals + `}`))
	if !errors.As(err, &ce) || ce.Agent != DockerUsageBadAnswer {
		t.Fatalf("bare summary: %v", err)
	}
}
