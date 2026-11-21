package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// host_file problems (api-v1 v1.44 item 8), for org owners and admins:
//   - needs_action when the last apply was rollback_failed. For a live,
//     declared file only an apply that succeeds or is unchanged resolves
//     it (hostFileApplied); the sweep resolves it only when the host is
//     gone, revoked or archived, or no longer declares the file.
//   - heads_up drift when a file applied once has been different, missing
//     or wrong_owner for 30 minutes. The sweep opens and resolves it.

// hostFileDriftAfter is how long drift lasts before the bell shows it.
const hostFileDriftAfter = 30 * time.Minute

func hostFileSubject(hostID uuid.UUID, name string) string {
	return hostID.String() + ":" + name
}

// hostFileHref is the host's Config files tab with the file open.
func hostFileHref(hostID uuid.UUID, name string) string {
	return "/hosts/" + hostID.String() + "?tab=files&files.name=" + url.QueryEscape(name)
}

// hostFileApplied opens the problem after rollback_failed and resolves it
// after an apply that succeeded or was unchanged.
func (s *Server) hostFileApplied(ctx context.Context, host store.Host, name, outcome string) {
	subject := hostFileSubject(host.ID, name)
	switch outcome {
	case store.HostFileRollbackFailed:
		id := host.ID
		s.openProblem(ctx, store.ProblemOpen{
			Kind: store.ProblemHostFile, Subject: subject, OrgID: host.OrgID, HostID: &id,
			Level: store.ProblemNeedsAction, Title: name + " on " + host.Name + " could not be restored",
			Detail: "The new file failed and the previous file could not be restored. Check the host.",
		})
	case store.HostFileSucceeded, store.HostFileUnchanged:
		s.resolveProblem(ctx, store.ProblemHostFile, subject, name+" on "+host.Name+" is applied", "A later apply succeeded.")
	}
}

// driftText is the problem text per drifted word (store.Drifted).
var driftText = map[string]struct{ title, detail string }{
	store.HostFileDifferent:  {"%s on %s differs from the applied version", "The file on the host was changed outside Deploybot."},
	store.HostFileMissing:    {"%s on %s is missing", "The file is gone from the host."},
	store.HostFileWrongOwner: {"%s on %s has the wrong owner or mode", "The file must be root-owned with the mode runner.toml sets."},
}

// sweepHostFiles is the drift check of every live host with a stored
// status, limited to the files its hello declares now. A stale status
// (older than 25 minutes) neither opens nor resolves. Afterwards every open
// row it did not check is resolved, at either level, when its host is
// gone, revoked or archived, or no longer declares the file; a host that
// could not be read keeps its rows for the next minute.
func (s *Server) sweepHostFiles(ctx context.Context, now time.Time) {
	rows, err := s.Store.ListHostFilesStatuses(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostFile)
		return
	}
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemHostFile})
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostFile)
		return
	}
	openLevel := map[string]string{}
	for _, p := range open {
		if p.ResolvedAt == nil {
			openLevel[p.Subject] = p.Level
		}
	}
	watched := map[string]bool{}
	for _, row := range rows {
		host, err := s.Store.GetHost(ctx, row.HostID)
		if err != nil || host.Programs == nil {
			continue // the leftover pass decides these rows
		}
		declared := map[string]bool{}
		for _, f := range host.Programs.HostFiles {
			declared[f.Name] = true
		}
		states, err := s.Store.HostFileStates(ctx, row.HostID)
		if err != nil {
			for name := range declared {
				watched[hostFileSubject(row.HostID, name)] = true
			}
			continue
		}
		byName := stateByName(states)
		for _, f := range row.Status.Files {
			if !declared[f.Name] {
				continue
			}
			subject := hostFileSubject(row.HostID, f.Name)
			watched[subject] = true
			if openLevel[subject] == store.ProblemNeedsAction {
				continue // rollback_failed: resolved only by an apply
			}
			word := store.FileWord(&row.Status, f.Name, now)
			if word == store.HostFileUnknown {
				continue
			}
			switch text, drifted := driftText[word]; {
			case drifted && byName[f.Name].AppliedVersion != nil && f.BadSince != nil && now.Sub(*f.BadSince) >= hostFileDriftAfter:
				id := row.HostID
				s.openProblem(ctx, store.ProblemOpen{
					Kind: store.ProblemHostFile, Subject: subject, OrgID: row.OrgID, HostID: &id,
					Level: store.ProblemHeadsUp, Title: fmt.Sprintf(text.title, f.Name, row.Name), Detail: text.detail,
				})
			case !store.Drifted(word) && openLevel[subject] == store.ProblemHeadsUp:
				s.resolveProblem(ctx, store.ProblemHostFile, subject, f.Name+" on "+row.Name+" matches the applied version", "In sync again.")
			}
		}
	}
	for _, p := range open {
		if p.ResolvedAt != nil || watched[p.Subject] || p.HostID == nil {
			continue
		}
		if s.hostFileGone(ctx, *p.HostID, p.Subject) {
			s.resolveProblem(ctx, store.ProblemHostFile, p.Subject, "", "No longer checked: the host was revoked or no longer declares the file.")
		}
	}
}

// hostFileGone reports whether nobody can act on the problem's file any
// more: the host is gone, revoked or archived, or no longer declares the
// file. A read error is false, so the row waits for the next minute.
func (s *Server) hostFileGone(ctx context.Context, hostID uuid.UUID, subject string) bool {
	host, err := s.Store.GetHost(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	if host.RevokedAt != nil || host.ArchivedAt != nil || host.Programs == nil {
		return true
	}
	_, name, _ := strings.Cut(subject, ":")
	for _, f := range host.Programs.HostFiles {
		if f.Name == name {
			return false
		}
	}
	return true
}
