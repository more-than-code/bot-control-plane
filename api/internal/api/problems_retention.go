package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// metrics_retention (api-v1 v1.58, spec-retention-self-check rev 2): the
// hourly sweep reads TimescaleDB's job stats for the retention policies of
// the metrics hypertables and opens one problem for the whole server when a
// job fails or stops running. Its audience is the owners and admins of the
// org that owns BOT_API_SERVER_HOST, read at each sweep, so the subject
// carries that org: a host in another org resolves the old problem and
// opens a new one.
const (
	// retentionFailingAfter: a job that ran but has no success this long.
	retentionFailingAfter = 48 * time.Hour
	// retentionOverdueAfter: a job whose next start is this far in the past.
	retentionOverdueAfter = time.Hour
	// retentionUrgentAfter: needs_action once a failure lasts the retention
	// window itself.
	retentionUrgentAfter = 7 * 24 * time.Hour

	retentionTitle         = "Metrics retention isn't running on this server"
	retentionResolvedTitle = "Metrics retention is running again"
)

func retentionSubject(org uuid.UUID) string {
	return store.ProblemMetricsRetention + ":" + org.String()
}

type retentionVerdict struct {
	bad    bool
	level  string
	detail string
}

// judgeRetention decides from the jobs (sorted by hypertable) at now; opened
// is when the open problem opened, nil when none is open.
func judgeRetention(jobs []store.RetentionJob, now time.Time, opened *time.Time) retentionVerdict {
	urgent := opened != nil && now.Sub(*opened) >= retentionUrgentAfter
	if len(jobs) == 0 {
		return retentionVerdict{bad: true, level: retentionLevel(urgent), detail: "No retention policies found: old metrics are never deleted."}
	}
	var tables []string
	reason := ""
	for _, j := range jobs {
		failing := j.TotalRuns > 0 && (j.LastSuccess == nil || now.Sub(*j.LastSuccess) > retentionFailingAfter)
		notRunning := j.NextStart == nil || now.Sub(*j.NextStart) > retentionOverdueAfter
		if !failing && !notRunning {
			continue
		}
		tables = append(tables, j.Hypertable)
		if j.LastSuccess != nil && now.Sub(*j.LastSuccess) >= retentionUrgentAfter {
			urgent = true
		}
		if reason != "" {
			continue
		}
		switch {
		case failing && j.LastSuccess == nil:
			reason = fmt.Sprintf("never succeeded, %d failures", j.TotalFailures)
		case failing:
			reason = fmt.Sprintf("last success %s ago, %d failures", spanText(now.Sub(*j.LastSuccess)), j.TotalFailures)
		case j.NextStart == nil:
			reason = "not scheduled"
		default:
			reason = "overdue " + spanText(now.Sub(*j.NextStart))
		}
	}
	if len(tables) == 0 {
		return retentionVerdict{}
	}
	names := strings.Join(tables, ", ")
	if len(tables) > 2 {
		names = fmt.Sprintf("%s, %s (+%d)", tables[0], tables[1], len(tables)-2)
	}
	return retentionVerdict{bad: true, level: retentionLevel(urgent), detail: names + ": " + reason + ". Old metrics are not being deleted."}
}

func retentionLevel(urgent bool) string {
	if urgent {
		return store.ProblemNeedsAction
	}
	return store.ProblemHeadsUp
}

// sweepMetricsRetention is the hourly check. With no server host set it
// does nothing; an unknown or revoked server host turns it off with one
// warning per hour and resolves any open problem. An unreadable view
// neither opens nor resolves.
func (s *Server) sweepMetricsRetention(ctx context.Context, now time.Time) {
	if s.ServerHostID == uuid.Nil {
		return
	}
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemMetricsRetention})
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemMetricsRetention)
		return
	}
	hosts, err := s.Store.ListProblemHosts(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemMetricsRetention)
		return
	}
	var server *store.ProblemHost
	for i := range hosts {
		if hosts[i].ID == s.ServerHostID {
			server = &hosts[i]
			break
		}
	}
	if server == nil {
		s.logger().Warn("metrics retention check is off: BOT_API_SERVER_HOST names no live host", "host_id", s.ServerHostID.String())
		for _, p := range open {
			s.resolveProblem(ctx, store.ProblemMetricsRetention, p.Subject, "", "No longer checked: the server host is unknown or revoked.")
		}
		return
	}
	jobs, err := s.Store.RetentionJobs(ctx)
	if err != nil {
		s.logger().Warn("metrics retention check could not read the job stats", "sqlstate", dbErrCode(err))
		return
	}
	subject := retentionSubject(server.OrgID)
	var opened *time.Time
	for _, p := range open {
		if p.Subject == subject {
			at := p.OpenedAt
			opened = &at
			continue
		}
		s.resolveProblem(ctx, store.ProblemMetricsRetention, p.Subject, "", "No longer checked here: the server host belongs to another organization now.")
	}
	v := judgeRetention(jobs, now, opened)
	if !v.bad {
		if opened != nil {
			s.resolveProblem(ctx, store.ProblemMetricsRetention, subject, retentionResolvedTitle, "Every retention job succeeded within the last 2 days.")
		}
		return
	}
	hostID := server.ID
	s.openProblem(ctx, store.ProblemOpen{
		Kind: store.ProblemMetricsRetention, Subject: subject, OrgID: server.OrgID, HostID: &hostID,
		Level: v.level, Title: retentionTitle, Detail: v.detail,
	})
}

// dbErrCode is the SQLSTATE of a database error ("42501" permission denied,
// "42P01" no such relation), or "unknown": a code helps the operator and,
// unlike the message, never names hosts, users or databases.
func dbErrCode(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) && coded.SQLState() != "" {
		return coded.SQLState()
	}
	return "unknown"
}
