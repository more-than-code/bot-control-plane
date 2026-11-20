package memstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Problems (api-v1 v1.41), as db/problems.go.

func (m *Mem) OpenProblem(_ context.Context, in store.ProblemOpen, at time.Time) (store.Problem, error) {
	if !store.ValidProblemOpen(in) {
		return store.Problem{}, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var reopen *store.Problem
	for id, p := range m.problems {
		if p.Kind != in.Kind || p.Subject != in.Subject {
			continue
		}
		if p.ResolvedAt == nil {
			if p.Level == in.Level && p.Title == in.Title && p.Detail == in.Detail {
				return cloneProblem(p), nil
			}
			p.Level, p.Title, p.Detail, p.UpdatedAt = in.Level, in.Title, in.Detail, at
			m.problems[id] = p
			return cloneProblem(p), nil
		}
		if !p.ResolvedAt.Before(at.Add(-store.ProblemReopenWindow)) && (reopen == nil || newerResolved(p, *reopen)) {
			c := p
			reopen = &c
		}
	}
	if reopen != nil {
		p := *reopen
		p.Level, p.Title, p.Detail, p.UpdatedAt, p.ResolvedAt = in.Level, in.Title, in.Detail, at, nil
		m.problems[p.ID] = p
		return cloneProblem(p), nil
	}
	p := store.Problem{
		ID: uuid.New(), Kind: in.Kind, Subject: in.Subject, OrgID: in.OrgID,
		ProjectID: copyID(in.ProjectID), HostID: copyID(in.HostID), EntryID: copyID(in.EntryID),
		Level: in.Level, Title: in.Title, Detail: in.Detail, OpenedAt: at, UpdatedAt: at,
	}
	m.problems[p.ID] = p
	return cloneProblem(p), nil
}

// newerResolved orders resolved rows as the DB does: resolved_at, then id.
func newerResolved(a, b store.Problem) bool {
	if !a.ResolvedAt.Equal(*b.ResolvedAt) {
		return a.ResolvedAt.After(*b.ResolvedAt)
	}
	return a.ID.String() > b.ID.String()
}

func (m *Mem) ResolveProblem(_ context.Context, kind, subject, title, detail string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.problems {
		if p.Kind != kind || p.Subject != subject || p.ResolvedAt != nil {
			continue
		}
		if title != "" {
			p.Title = title
		}
		if detail != "" {
			p.Detail = detail
		}
		t := at
		p.ResolvedAt, p.UpdatedAt = &t, at
		m.problems[id] = p
		return true, nil
	}
	return false, nil
}

func (m *Mem) ListProblems(_ context.Context, f store.ProblemFilter) ([]store.Problem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	orgs, projects := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, id := range f.OrgIDs {
		orgs[id] = true
	}
	for _, id := range f.ProjectIDs {
		projects[id] = true
	}
	out := []store.Problem{}
	for _, p := range m.problems {
		if !f.All && !orgs[p.OrgID] && (p.ProjectID == nil || !projects[*p.ProjectID]) {
			continue
		}
		if f.Kind != "" && p.Kind != f.Kind {
			continue
		}
		if p.ResolvedAt != nil && (f.ResolvedSince == nil || p.ResolvedAt.Before(*f.ResolvedSince)) {
			continue
		}
		out = append(out, cloneProblem(p))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := problemChanged(out[i]), problemChanged(out[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func problemChanged(p store.Problem) time.Time {
	if p.ResolvedAt != nil {
		return *p.ResolvedAt
	}
	return p.UpdatedAt
}

func (m *Mem) PruneProblems(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, p := range m.problems {
		if p.ResolvedAt != nil && p.ResolvedAt.Before(before) {
			delete(m.problems, id)
			n++
		}
	}
	return n, nil
}

func (m *Mem) ListProblemHosts(_ context.Context) ([]store.ProblemHost, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.ProblemHost{}
	for _, h := range m.hosts {
		if h.RevokedAt != nil {
			continue
		}
		out = append(out, store.ProblemHost{ID: h.ID, OrgID: h.OrgID, Name: h.Name, AlwaysOn: h.AlwaysOn, LastSeenAt: copyTime(h.LastSeenAt)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out, nil
}

func (m *Mem) ListRegistryLogins(_ context.Context) ([]store.RegistryLogin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.RegistryLogin{}
	for key := range m.registries {
		pid, host, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		id, err := uuid.Parse(pid)
		if err != nil {
			continue
		}
		p, ok := m.projects[id]
		if !ok || p.DeletedAt != nil {
			continue
		}
		out = append(out, store.RegistryLogin{ProjectID: id, OrgID: p.OrgID, Host: host})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID.String() < out[j].ProjectID.String()
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

// dropEntryProblemsLocked is the entry foreign key cascade (migration 0027).
func (m *Mem) dropEntryProblemsLocked(entryID uuid.UUID) {
	for id, p := range m.problems {
		if p.EntryID != nil && *p.EntryID == entryID {
			delete(m.problems, id)
		}
	}
}

func cloneProblem(p store.Problem) store.Problem {
	p.ProjectID, p.HostID, p.EntryID, p.ResolvedAt = copyID(p.ProjectID), copyID(p.HostID), copyID(p.EntryID), copyTime(p.ResolvedAt)
	return p
}

func copyID(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		return nil
	}
	c := *id
	return &c
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
