package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.58 (spec-retention-self-check rev 2): the hourly sweep opens
// metrics_retention when a TimescaleDB retention job fails or stops running,
// for the owners and admins of the org that owns BOT_API_SERVER_HOST.

func retentionJob(table string, lastOK *time.Time, runs, fails int64, next *time.Time) store.RetentionJob {
	return store.RetentionJob{JobID: 1000, Hypertable: table, LastSuccess: lastOK, TotalRuns: runs, TotalFailures: fails, NextStart: next}
}

func tp(t time.Time) *time.Time { return &t }

func TestJudgeRetention(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	healthy := retentionJob("host_cpu", tp(now.Add(-2*time.Hour)), 40, 0, tp(now.Add(22*time.Hour)))
	for _, tc := range []struct {
		name   string
		jobs   []store.RetentionJob
		opened *time.Time
		bad    bool
		level  string
		detail string
	}{
		{"all healthy", []store.RetentionJob{healthy}, nil, false, "", ""},
		{"failing 49 h", []store.RetentionJob{healthy, retentionJob("host_net", tp(now.Add(-49*time.Hour)), 30, 4, tp(now.Add(time.Hour)))}, nil,
			true, store.ProblemHeadsUp, "host_net: last success 2 d ago, 4 failures. Old metrics are not being deleted."},
		{"failing 8 d", []store.RetentionJob{retentionJob("host_net", tp(now.Add(-8*24*time.Hour)), 30, 9, tp(now.Add(time.Hour)))}, nil,
			true, store.ProblemNeedsAction, "host_net: last success 8 d ago, 9 failures. Old metrics are not being deleted."},
		{"never succeeded", []store.RetentionJob{retentionJob("host_cpu", nil, 2720, 2720, tp(now.Add(time.Hour)))}, nil,
			true, store.ProblemHeadsUp, "host_cpu: never succeeded, 2720 failures. Old metrics are not being deleted."},
		{"never succeeded, open 7 d", []store.RetentionJob{retentionJob("host_cpu", nil, 2720, 2720, tp(now.Add(time.Hour)))}, tp(now.Add(-7 * 24 * time.Hour)),
			true, store.ProblemNeedsAction, "host_cpu: never succeeded, 2720 failures. Old metrics are not being deleted."},
		{"no next_start", []store.RetentionJob{retentionJob("host_temp", nil, 0, 0, nil)}, nil,
			true, store.ProblemHeadsUp, "host_temp: not scheduled. Old metrics are not being deleted."},
		{"2 h overdue", []store.RetentionJob{retentionJob("host_temp", nil, 0, 0, tp(now.Add(-2*time.Hour)))}, nil,
			true, store.ProblemHeadsUp, "host_temp: overdue 2 h. Old metrics are not being deleted."},
		{"overdue under 1 h", []store.RetentionJob{retentionJob("host_temp", nil, 0, 0, tp(now.Add(-30*time.Minute)))}, nil, false, "", ""},
		{"many tables", []store.RetentionJob{
			retentionJob("a", nil, 0, 0, nil), retentionJob("b", nil, 0, 0, nil), retentionJob("c", nil, 0, 0, nil), healthy}, nil,
			true, store.ProblemHeadsUp, "a, b (+1): not scheduled. Old metrics are not being deleted."},
		{"no jobs", nil, nil, true, store.ProblemHeadsUp, "No retention policies found: old metrics are never deleted."},
	} {
		v := judgeRetention(tc.jobs, now, tc.opened)
		if v.bad != tc.bad || v.level != tc.level || v.detail != tc.detail {
			t.Fatalf("%s: %+v", tc.name, v)
		}
	}
}

type retentionScene struct {
	notificationScene
	clock func(time.Time)
}

func newRetentionScene(t *testing.T) retentionScene {
	s := newNotificationScene(t)
	set := problemClock(s.w)
	set(s.w.now.Add(10 * time.Minute))
	s.w.srv.sweep.start = s.w.now.Add(-time.Hour) // past the sweep grace
	s.w.srv.ServerHostID = s.current.ID
	return retentionScene{notificationScene: s, clock: set}
}

// problems is the caller's metrics_retention problems in the scene's org.
func (s retentionScene) problems(t *testing.T, as uuid.UUID) []problemWire {
	t.Helper()
	var out []problemWire
	for _, p := range getProblems(t, s.w, s.w.token(as), "/v1/orgs/"+s.project.OrgID.String()+"/problems?resolved=1") {
		if p.Kind == store.ProblemMetricsRetention {
			out = append(out, p)
		}
	}
	return out
}

func TestMetricsRetentionProblem(t *testing.T) {
	s := newRetentionScene(t)
	ctx := context.Background()
	now := s.w.srv.now()
	s.w.mem.SetRetentionJobs([]store.RetentionJob{retentionJob("host_cpu", nil, 2720, 2720, tp(now.Add(time.Hour)))}, nil)
	sweepNow(ctx, s.w.srv)

	items := s.problems(t, s.owner.ID)
	if len(items) != 1 || items[0].Kind != store.ProblemMetricsRetention || items[0].Level != store.ProblemHeadsUp ||
		items[0].Title != "Metrics retention isn't running on this server" ||
		items[0].HostID == nil || *items[0].HostID != s.current.ID.String() ||
		items[0].Href != "/orgs/"+s.project.OrgID.String()+"?tab=settings#metrics-retention" {
		t.Fatalf("owner %+v", items)
	}
	if got := s.problems(t, s.admin.ID); len(got) != 1 {
		t.Fatalf("org admin %+v", got)
	}
	// An org member below admin, and an admin of another org, do not see it.
	for _, u := range []uuid.UUID{s.member.ID} {
		for _, p := range getProblems(t, s.w, s.w.token(u), "/v1/projects/"+s.project.ID.String()+"/problems") {
			if p.Kind == store.ProblemMetricsRetention {
				t.Fatalf("member sees %+v", p)
			}
		}
	}
	stranger := s.w.user("stranger@ex.com", "Stranger")
	strangerOrg := s.w.mem.PersonalOrg(stranger.ID)
	for _, p := range getProblems(t, s.w, s.w.token(stranger.ID), "/v1/orgs/"+strangerOrg.String()+"/problems") {
		if p.Kind == store.ProblemMetricsRetention {
			t.Fatalf("other org admin sees %+v", p)
		}
	}
	// A project admin who is not an org owner or admin does not see it.
	items = getProblems(t, s.w, s.w.token(s.projOnly.ID), "/v1/projects/"+s.project.ID.String()+"/problems")
	for _, it := range items {
		if it.Kind == store.ProblemMetricsRetention {
			t.Fatalf("project admin sees %+v", it)
		}
	}

	// An unreadable view keeps the open problem as it is.
	s.w.mem.SetRetentionJobs(nil, errors.New("permission denied for view job_stats"))
	s.clock(now.Add(2 * time.Hour))
	sweepNow(ctx, s.w.srv)
	if got := s.problems(t, s.owner.ID); len(got) != 1 || got[0].ResolvedAt != nil {
		t.Fatalf("unread %+v", got)
	}

	// Recovery resolves it.
	ok := s.w.srv.now()
	s.w.mem.SetRetentionJobs([]store.RetentionJob{retentionJob("host_cpu", tp(ok), 2721, 2720, tp(ok.Add(24*time.Hour)))}, nil)
	s.clock(now.Add(4 * time.Hour))
	sweepNow(ctx, s.w.srv)
	got := s.problems(t, s.owner.ID)
	if len(got) != 1 || got[0].ResolvedAt == nil || got[0].Title != "Metrics retention is running again" {
		t.Fatalf("resolved %+v", got)
	}
}

// The audience follows the host: an open problem filed under another org
// (the host's org before) is resolved, and a new one opens for the host's
// org now.
func TestMetricsRetentionFollowsHostOrg(t *testing.T) {
	s := newRetentionScene(t)
	ctx := context.Background()
	now := s.w.srv.now()
	other := s.w.mem.PersonalOrg(s.member.ID)
	if _, err := s.w.mem.OpenProblem(ctx, store.ProblemOpen{
		Kind: store.ProblemMetricsRetention, Subject: retentionSubject(other), OrgID: other,
		Level: store.ProblemHeadsUp, Title: "Metrics retention isn't running on this server", Detail: "x",
	}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.w.mem.SetRetentionJobs([]store.RetentionJob{retentionJob("host_cpu", nil, 0, 0, nil)}, nil)
	sweepNow(ctx, s.w.srv)
	all, err := s.w.mem.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemMetricsRetention, ResolvedSince: tp(now.Add(-24 * time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	var openHere, resolvedOther bool
	for _, p := range all {
		if p.OrgID == s.project.OrgID && p.ResolvedAt == nil {
			openHere = true
		}
		if p.OrgID == other && p.ResolvedAt != nil {
			resolvedOther = true
		}
	}
	if !openHere || !resolvedOther {
		t.Fatalf("%+v", all)
	}
}

// Unset, unknown or revoked server host: the check is off, nothing opens,
// and an unknown or revoked host logs a warning.
func TestMetricsRetentionOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(s retentionScene)
		warn bool
	}{
		{"unset", func(s retentionScene) { s.w.srv.ServerHostID = uuid.Nil }, false},
		{"unknown", func(s retentionScene) { s.w.srv.ServerHostID = uuid.New() }, true},
		{"revoked", func(s retentionScene) { s.w.srv.ServerHostID = s.revoked.ID }, true},
	} {
		var buf bytes.Buffer
		s := newRetentionScene(t)
		s.w.srv.Log = slog.New(slog.NewTextHandler(&buf, nil))
		tc.set(s)
		s.w.mem.SetRetentionJobs(nil, nil)
		sweepNow(context.Background(), s.w.srv)
		for _, p := range s.problems(t, s.owner.ID) {
			if p.Kind == store.ProblemMetricsRetention {
				t.Fatalf("%s: %+v", tc.name, p)
			}
		}
		if got := strings.Contains(buf.String(), "metrics retention check is off"); got != tc.warn {
			t.Fatalf("%s: log %q", tc.name, buf.String())
		}
	}
}

// An unknown server host resolves a problem that is still open.
func TestMetricsRetentionUnknownHostResolves(t *testing.T) {
	s := newRetentionScene(t)
	ctx := context.Background()
	now := s.w.srv.now()
	s.w.mem.SetRetentionJobs([]store.RetentionJob{retentionJob("host_cpu", nil, 0, 0, nil)}, nil)
	sweepNow(ctx, s.w.srv)
	if got := s.problems(t, s.owner.ID); len(got) != 1 || got[0].ResolvedAt != nil {
		t.Fatalf("open %+v", got)
	}
	s.w.srv.ServerHostID = uuid.New()
	s.clock(now.Add(2 * time.Hour))
	sweepNow(ctx, s.w.srv)
	got := s.problems(t, s.owner.ID)
	if len(got) != 1 || got[0].ResolvedAt == nil || got[0].Detail != "No longer checked: the server host is unknown or revoked." {
		t.Fatalf("resolved %+v", got)
	}
}
