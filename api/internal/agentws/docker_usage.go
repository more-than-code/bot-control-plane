package agentws

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// DockerUsageTimeoutSec is the docker_usage task budget (agent-protocol
// v1.25): the helper's 30 s engine deadline plus a margin.
const DockerUsageTimeoutSec = 45

// Docker usage reasons (agent-protocol v1.25). DockerUsageBadAnswer is the
// API's own: the answer could not be read as a summary.
const (
	DockerUsageDisabled    = "docker_usage_disabled"
	DockerUsageUnavailable = "docker_unavailable"
	DockerUsageTimeout     = "timeout"
	DockerUsageBadAnswer   = "bad_answer"
)

// dockerUsageListMax and dockerUsageStringMax are the summary caps
// (spec-disk-usage-breakdown §10); the API applies them again, after
// sorting each list again by size (largest first, unknown last), then by
// name (images by ref, then id).
const (
	dockerUsageListMax   = 10
	dockerUsageStringMax = 200
)

// reasonRe is a reason code's shape: never free text.
var reasonRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// DockerUsageTotal is one category's totals, in bytes for the sizes.
type DockerUsageTotal struct {
	Count       int64 `json:"count"`
	Active      int64 `json:"active"`
	Size        int64 `json:"size"`
	Reclaimable int64 `json:"reclaimable"`
}

// DockerUsageTotals are the four categories.
type DockerUsageTotals struct {
	Images     DockerUsageTotal `json:"images"`
	Containers DockerUsageTotal `json:"containers"`
	Volumes    DockerUsageTotal `json:"volumes"`
	BuildCache DockerUsageTotal `json:"build_cache"`
}

// DockerUsageImage is one of the largest images. Size is nil when Docker
// did not know it.
type DockerUsageImage struct {
	Ref        string `json:"ref"`
	ID         string `json:"id"`
	Size       *int64 `json:"size"`
	Containers int64  `json:"containers"`
}

// DockerUsageVolume is one of the largest volumes.
type DockerUsageVolume struct {
	Name  string `json:"name"`
	Size  *int64 `json:"size"`
	InUse bool   `json:"in_use"`
}

// DockerUsageContainer is one of the containers with the largest writable
// layer.
type DockerUsageContainer struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Size  *int64 `json:"size"`
}

// DockerUsageSummary is the docker_usage answer (agent-protocol v1.25),
// read field by field so nothing else the agent sent reaches the caller.
type DockerUsageSummary struct {
	Totals     DockerUsageTotals      `json:"totals"`
	Images     []DockerUsageImage     `json:"images"`
	Volumes    []DockerUsageVolume    `json:"volumes"`
	Containers []DockerUsageContainer `json:"containers"`
}

// DockerUsage asks a live host for its Docker disk usage (task
// docker_usage, spec {}). A failure is a *CallError whose Agent is the
// reason: docker_usage_disabled, docker_unavailable, timeout (the agent's
// own timeout or no answer before ctx ended), bad_answer, another agent
// code, or "" when the host is unavailable.
func (h *Hub) DockerUsage(ctx context.Context, host store.Host) (DockerUsageSummary, error) {
	res, err := h.live(ctx, host, "docker_usage", nil, map[string]any{}, DockerUsageTimeoutSec)
	if err != nil {
		var ce *CallError
		if !errors.As(err, &ce) {
			return DockerUsageSummary{}, err
		}
		reason := ce.Agent
		if reason == "" {
			reason = payloadReason(ce.Payload)
		}
		if ce.Code == "timeout" && reason == "" {
			reason = DockerUsageTimeout
		}
		if reason == "" && ce.Code == "unavailable" {
			return DockerUsageSummary{}, ce
		}
		return DockerUsageSummary{}, &CallError{Code: ce.Code, Msg: ce.Msg, Agent: reason, FromResult: ce.FromResult}
	}
	return ParseDockerUsage(res.Payload)
}

// ParseDockerUsage reads a succeeded docker_usage payload, exactly as
// agent-protocol v1.25 item 1 sends it: {"outcome":"ok","summary":{…}}.
// {"outcome":"failed","reason":…} is read as that reason; anything else
// is bad_answer.
func ParseDockerUsage(raw json.RawMessage) (DockerUsageSummary, error) {
	var top struct {
		Outcome string          `json:"outcome"`
		Reason  string          `json:"reason"`
		Summary json.RawMessage `json:"summary"`
	}
	bad := &CallError{Code: "bad_gateway", Msg: "bad answer", Agent: DockerUsageBadAnswer}
	if json.Unmarshal(raw, &top) != nil {
		return DockerUsageSummary{}, bad
	}
	switch {
	case top.Outcome == "failed":
		reason := top.Reason
		if !reasonRe.MatchString(reason) {
			reason = "error"
		}
		return DockerUsageSummary{}, &CallError{Code: "error", Msg: reason, Agent: reason, FromResult: true}
	case top.Outcome != "ok":
		return DockerUsageSummary{}, bad
	}
	var body struct {
		Totals     *DockerUsageTotals `json:"totals"`
		Images     []json.RawMessage  `json:"images"`
		Volumes    []json.RawMessage  `json:"volumes"`
		Containers []json.RawMessage  `json:"containers"`
	}
	if json.Unmarshal(top.Summary, &body) != nil || body.Totals == nil {
		return DockerUsageSummary{}, bad
	}
	t := *body.Totals
	for _, c := range []*DockerUsageTotal{&t.Images, &t.Containers, &t.Volumes, &t.BuildCache} {
		c.Count, c.Active, c.Size, c.Reclaimable = nonNeg(c.Count), nonNeg(c.Active), nonNeg(c.Size), nonNeg(c.Reclaimable)
	}
	images := entries(body.Images, func(im DockerUsageImage) DockerUsageImage {
		return DockerUsageImage{Ref: capString(im.Ref), ID: capString(im.ID), Size: sizeOrNil(im.Size), Containers: nonNeg(im.Containers)}
	})
	sort.SliceStable(images, func(i, j int) bool {
		a, b := images[i], images[j]
		if c := cmpSize(a.Size, b.Size); c != 0 {
			return c < 0
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.ID < b.ID
	})
	volumes := entries(body.Volumes, func(v DockerUsageVolume) DockerUsageVolume {
		return DockerUsageVolume{Name: capString(v.Name), Size: sizeOrNil(v.Size), InUse: v.InUse}
	})
	sort.SliceStable(volumes, func(i, j int) bool {
		if c := cmpSize(volumes[i].Size, volumes[j].Size); c != 0 {
			return c < 0
		}
		return volumes[i].Name < volumes[j].Name
	})
	containers := entries(body.Containers, func(c DockerUsageContainer) DockerUsageContainer {
		return DockerUsageContainer{Name: capString(c.Name), State: capString(c.State), Size: sizeOrNil(c.Size)}
	})
	sort.SliceStable(containers, func(i, j int) bool {
		if c := cmpSize(containers[i].Size, containers[j].Size); c != 0 {
			return c < 0
		}
		return containers[i].Name < containers[j].Name
	})
	return DockerUsageSummary{
		Totals:     t,
		Images:     capList(images),
		Volumes:    capList(volumes),
		Containers: capList(containers),
	}, nil
}

// payloadReason is the "reason" of a failed result's payload, "" when
// there is none or it is not a short code.
func payloadReason(raw json.RawMessage) string {
	var p struct {
		Reason string `json:"reason"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil || !reasonRe.MatchString(p.Reason) {
		return ""
	}
	return p.Reason
}

// entries decodes each list entry on its own and cleans it. A null entry,
// or one with a wrong-typed field, is dropped; the rest stay.
func entries[T any](raw []json.RawMessage, clean func(T) T) []T {
	out := make([]T, 0, len(raw))
	for _, r := range raw {
		if string(r) == "null" {
			continue
		}
		var v T
		if json.Unmarshal(r, &v) != nil {
			continue
		}
		out = append(out, clean(v))
	}
	return out
}

// capList keeps the first dockerUsageListMax entries.
func capList[T any](s []T) []T {
	if len(s) > dockerUsageListMax {
		return s[:dockerUsageListMax]
	}
	return s
}

// cmpSize orders sizes largest first, unknown (nil) last: negative when a
// comes first.
func cmpSize(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a > *b:
		return -1
	case *a < *b:
		return 1
	}
	return 0
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// sizeOrNil keeps an unknown (nil or negative) size as nil.
func sizeOrNil(n *int64) *int64 {
	if n == nil || *n < 0 {
		return nil
	}
	v := *n
	return &v
}

// capString cuts s at dockerUsageStringMax characters and drops invalid
// UTF-8.
func capString(s string) string {
	if !utf8.ValidString(s) {
		s = string([]rune(s))
	}
	if utf8.RuneCountInString(s) <= dockerUsageStringMax {
		return s
	}
	return string([]rune(s)[:dockerUsageStringMax])
}
