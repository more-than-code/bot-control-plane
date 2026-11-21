package store

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Problems in the bell (api-v1 v1.41, migration 0027).

// Problem kinds.
const (
	ProblemHostOffline   = "host_offline"
	ProblemRegistryLogin = "registry_login"
	ProblemDeploy        = "deploy"
	// ProblemHostFile is a host config file (api-v1 v1.44 item 8).
	ProblemHostFile = "host_file"
)

// Problem levels.
const (
	ProblemNeedsAction = "needs_action"
	ProblemHeadsUp     = "heads_up"
)

const (
	// MaxProblemText is the longest title or detail, in characters.
	MaxProblemText = 300
	// ProblemReopenWindow: a problem that returns within this long of
	// resolving reopens the same row.
	ProblemReopenWindow = 30 * time.Minute
	// ProblemRetention is how long a resolved problem is kept.
	ProblemRetention = 30 * 24 * time.Hour
)

// Problem is one stored problem. ResolvedAt is nil while it is open.
type Problem struct {
	ID         uuid.UUID
	Kind       string
	Subject    string
	OrgID      uuid.UUID
	ProjectID  *uuid.UUID
	HostID     *uuid.UUID
	EntryID    *uuid.UUID
	Level      string
	Title      string
	Detail     string
	OpenedAt   time.Time
	UpdatedAt  time.Time
	ResolvedAt *time.Time
}

// ProblemOpen is what a detector reports while a problem lasts.
type ProblemOpen struct {
	Kind      string
	Subject   string
	OrgID     uuid.UUID
	ProjectID *uuid.UUID
	HostID    *uuid.UUID
	EntryID   *uuid.UUID
	Level     string
	Title     string
	Detail    string
}

// ProblemFilter selects problems. All ignores OrgIDs and ProjectIDs (the
// sweep); otherwise a row matches when its org is in OrgIDs or its project
// is in ProjectIDs. Kind "" is every kind. Open rows always match;
// ResolvedSince non-nil adds rows resolved at or after it.
type ProblemFilter struct {
	All           bool
	OrgIDs        []uuid.UUID
	ProjectIDs    []uuid.UUID
	Kind          string
	ResolvedSince *time.Time
}

// ProblemHost is one live host the sweep checks: not revoked (so not archived).
type ProblemHost struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Name       string
	AlwaysOn   bool
	LastSeenAt *time.Time
}

// RegistryLogin is one stored registry login of a live project.
type RegistryLogin struct {
	ProjectID uuid.UUID
	OrgID     uuid.UUID
	Host      string
}

// ValidProblemOpen checks kind, level, a subject of 1 to 300 characters, a
// title of 1 to 300 and a detail of at most 300.
func ValidProblemOpen(in ProblemOpen) bool {
	switch in.Kind {
	case ProblemHostOffline, ProblemRegistryLogin, ProblemDeploy, ProblemHostFile:
	default:
		return false
	}
	if in.Level != ProblemNeedsAction && in.Level != ProblemHeadsUp {
		return false
	}
	if in.OrgID == uuid.Nil {
		return false
	}
	s, t, d := utf8.RuneCountInString(in.Subject), utf8.RuneCountInString(in.Title), utf8.RuneCountInString(in.Detail)
	return s >= 1 && s <= MaxProblemText && t >= 1 && t <= MaxProblemText && d <= MaxProblemText &&
		utf8.ValidString(in.Subject) && utf8.ValidString(in.Title) && utf8.ValidString(in.Detail)
}

// ProblemStore keeps problems (api-v1 v1.41 items 1 and 6).
type ProblemStore interface {
	// OpenProblem opens the problem or updates it while it lasts: the open
	// row for (kind, subject) gets level, title, detail and updated_at (an
	// open row that already has that level, title and detail is returned
	// as it is, without a write); with
	// none, the newest row resolved within ProblemReopenWindow before at
	// reopens (resolved_at null, opened_at kept); otherwise a new row opens
	// at at. Concurrent calls for one (kind, subject) leave one open row.
	// An input that fails ValidProblemOpen is ErrInvalid.
	OpenProblem(ctx context.Context, in ProblemOpen, at time.Time) (Problem, error)
	// ResolveProblem resolves the open row for (kind, subject) at at,
	// replacing title and detail when they are not empty. It reports
	// whether a row was open.
	ResolveProblem(ctx context.Context, kind, subject, title, detail string, at time.Time) (bool, error)
	// ListProblems returns the rows f selects, the latest change first
	// (resolved_at, else updated_at), then by id.
	ListProblems(ctx context.Context, f ProblemFilter) ([]Problem, error)
	// PruneProblems deletes rows resolved before before.
	PruneProblems(ctx context.Context, before time.Time) (int64, error)
	// ListProblemHosts returns every host that is not revoked.
	ListProblemHosts(ctx context.Context) ([]ProblemHost, error)
	// ListRegistryLogins returns every stored registry login of a project
	// that is not deleted, by project then host.
	ListRegistryLogins(ctx context.Context) ([]RegistryLogin, error)
}
