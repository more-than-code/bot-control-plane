package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Domains, the per-host edge and routes (api-v1 v1.35, migration 0023).

// Domain statuses (api-v1 v1.35 item 1).
const (
	DomainPending  = "pending"
	DomainVerified = "verified"
	DomainLapsed   = "lapsed"
)

// DomainLapseAfter is how many consecutive failed daily re-checks turn a
// verified domain lapsed (spec D4).
const DomainLapseAfter = 7

// Edge statuses (api-v1 v1.35 item 2).
const (
	EdgeOff     = "off"
	EdgePending = "pending"
	EdgeApplied = "applied"
	EdgeError   = "error"
)

// Edge confirmation kinds.
const (
	EdgePlanRoutes       = "routes"
	EdgePlanDomainDelete = "domain_delete"
)

// ErrDomainTaken is a verify (or add) of a name another org has verified.
var ErrDomainTaken = errors.New("this domain is verified by another organization")

// ErrRoutesChanged is a domain delete whose routes differ from its plan.
var ErrRoutesChanged = errors.New("routes changed since the plan; ask for a new one")

// RouteKeys is the sorted hostname+path keys of routes.
func RouteKeys(routes []Route) []string {
	out := make([]string, len(routes))
	for i, r := range routes {
		out[i] = r.Key()
	}
	sort.Strings(out)
	return out
}

// OrgDomain is one domain of an org. Assignments are sorted by project
// name, then pattern.
type OrgDomain struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	Name        string
	Token       string
	Status      string
	VerifiedAt  *time.Time
	LastCheckAt *time.Time
	Failures    int
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
	Assignments []DomainAssignment
}

// DomainAssignment lets a project route under a domain. Pattern is
// relative to the domain name, like a DNS zone file (api-v1 v1.52): "@" (the
// name itself), "*" (one label below it), a relative name such as
// "marketingos" or "api.dev" (exactly that name below the domain), or "*."
// and a relative name such as "*.dev" (one label below that name).
type DomainAssignment struct {
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Pattern     string    `json:"pattern"`
}

// patternLabelRe is one LDH label of a relative assignment name.
var patternLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidPattern reports whether pattern is an assignment pattern of the
// domain: "@", "*", a relative name or "*." and a relative name, whose full
// name is at most 253 characters.
func ValidPattern(domain, pattern string) bool {
	if pattern == "@" || pattern == "*" {
		return true
	}
	rel := strings.TrimPrefix(pattern, "*.")
	if rel == "" || len(PatternName(domain, pattern)) > 253 {
		return false
	}
	for _, l := range strings.Split(rel, ".") {
		if !patternLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// PatternName is the name a pattern assigns, written out: "example.com",
// "*.example.com", "app.example.com" or "*.dev.example.com".
func PatternName(domain, pattern string) string {
	if pattern == "@" {
		return domain
	}
	return pattern + "." + domain
}

// Covers reports whether hostname falls under the domain through pattern.
func Covers(domain, pattern, hostname string) bool {
	switch {
	case pattern == "@":
		return hostname == domain
	case pattern == "*":
		label, rest, ok := strings.Cut(hostname, ".")
		return ok && label != "" && rest == domain
	case strings.HasPrefix(pattern, "*."):
		label, rest, ok := strings.Cut(hostname, ".")
		return ok && label != "" && rest == pattern[2:]+"."+domain
	case pattern == "" || strings.Contains(pattern, "*"):
		return false
	default:
		return hostname == pattern+"."+domain
	}
}

// UnderDomain reports whether hostname is the domain or any name below it:
// the routes a domain's assignments can cover.
func UnderDomain(domain, hostname string) bool {
	return hostname == domain || strings.HasSuffix(hostname, "."+domain)
}

// OneLabelUnder reports whether hostname is the domain or exactly one label
// below it: what @ and * reach.
func OneLabelUnder(domain, hostname string) bool {
	return Covers(domain, "@", hostname) || Covers(domain, "*", hostname)
}

// AssignPattern is the narrowest pattern that assigns exactly hostname
// under domain: "@" for the name itself, else the relative name. ok is false
// when hostname is not under the domain.
func AssignPattern(domain, hostname string) (string, bool) {
	if hostname == domain {
		return "@", true
	}
	rel, ok := strings.CutSuffix(hostname, "."+domain)
	if !ok || rel == "" {
		return "", false
	}
	return rel, true
}

// Route is one public address of a catalog entry. PathPrefix is "/" or
// "/seg[/seg…]". NoStorePaths is never nil after a read.
type Route struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	EntryID      uuid.UUID
	Entry        string
	Hostname     string
	PathPrefix   string
	Port         int
	ClientIPFrom *string
	NoStorePaths []string
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
}

// Key is hostname plus path prefix, unique across the instance.
func (r Route) Key() string { return r.Hostname + r.PathPrefix }

// HostEdge is the edge state of one host. A host with no row reads as
// disabled and off.
type HostEdge struct {
	HostID     uuid.UUID
	Enabled    bool
	Status     string
	Detail     string
	ConfigHash string
	AppliedAt  *time.Time
	UpdatedAt  time.Time
}

// HostEntry is a catalog entry with a live owned service on a host. Ports
// is the union of the listeners of the entry's own live owned rows there,
// sorted: the only ports a route of the entry may reach on that host.
type HostEntry struct {
	ProjectID uuid.UUID
	Entry     string
	Ports     []int
}

// EdgeConfirmation is a single-use routes plan (ProjectID and EntryID set)
// or domain delete plan (OrgID and DomainID set), stored by token hash.
type EdgeConfirmation struct {
	UserID         uuid.UUID
	Kind           string
	ProjectID      *uuid.UUID
	EntryID        *uuid.UUID
	OrgID          *uuid.UUID
	DomainID       *uuid.UUID
	Body           json.RawMessage
	RequestHash    []byte
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
}

// EdgeStore is the persistence of domains, routes and host edges. Every
// read treats rows of a deleted org or project as missing.
type EdgeStore interface {
	// ListOrgDomains returns the org's domains sorted by name, with assignments.
	ListOrgDomains(ctx context.Context, orgID uuid.UUID) ([]OrgDomain, error)
	// GetOrgDomain returns one domain of the org, or ErrNotFound.
	GetOrgDomain(ctx context.Context, orgID, domainID uuid.UUID) (OrgDomain, error)
	// CreateOrgDomain adds a pending domain. The same name in the org is
	// ErrConflict; a name another org holds verified or lapsed is ErrDomainTaken.
	CreateOrgDomain(ctx context.Context, orgID uuid.UUID, name, token string, actor uuid.UUID, at time.Time) (OrgDomain, error)
	// MarkDomainVerified sets verified, failures 0 and last_check_at. verified_at
	// is set when the domain was not verified before. ErrDomainTaken when
	// another org holds the name.
	MarkDomainVerified(ctx context.Context, orgID, domainID uuid.UUID, at time.Time) (OrgDomain, error)
	// RecordDomainCheck stores one daily re-check of a verified or lapsed
	// domain: ok sets verified and failures 0; a failure adds one and turns
	// the domain lapsed at DomainLapseAfter. lapsed is true when this call
	// made it lapsed. A pending domain is ErrState.
	RecordDomainCheck(ctx context.Context, domainID uuid.UUID, ok bool, at time.Time) (domain OrgDomain, lapsed bool, err error)
	// ListDomainsDue is the verified and lapsed domains of live orgs whose
	// last check is missing or before before, oldest first, at most limit.
	ListDomainsDue(ctx context.Context, before time.Time, limit int) ([]OrgDomain, error)
	// PutDomainAssignments replaces the domain's assignments. Every project
	// must be a live project of the org (ErrInvalid otherwise).
	PutDomainAssignments(ctx context.Context, orgID, domainID uuid.UUID, in []DomainAssignment) (OrgDomain, error)
	// ProjectDomains is the domains of the project's org with at least one
	// assignment to the project; Assignments holds only those.
	ProjectDomains(ctx context.Context, projectID uuid.UUID) ([]OrgDomain, error)
	// DeleteOrgDomain applies a confirmed domain delete plan in one
	// transaction: it checks the plan (missing, used, expired, foreign or
	// another domain's plan is ErrNotFound), compares the routes that would
	// stop (RoutesUnderDomain) with want, their sorted keys from the plan
	// (a difference is ErrRoutesChanged and leaves the plan unused), removes
	// those routes, the domain and its assignments, and marks the plan used.
	// It returns the removed routes.
	DeleteOrgDomain(ctx context.Context, orgID, domainID uuid.UUID, want []string, confirm []byte, actor uuid.UUID, at time.Time) ([]Route, error)
	// RoutesUnderDomain is the routes a domain delete stops, sorted by
	// hostname and path: routes of the org's live projects whose hostname
	// is under the domain (UnderDomain), except a hostname that another
	// verified or lapsed domain assigned to the same project still covers.
	RoutesUnderDomain(ctx context.Context, orgID, domainID uuid.UUID) ([]Route, error)

	// ListProjectRoutes returns the project's routes sorted by entry,
	// hostname and path prefix.
	ListProjectRoutes(ctx context.Context, projectID uuid.UUID) ([]Route, error)
	// ListRoutesByHostname returns the routes of live projects for these hostnames.
	ListRoutesByHostname(ctx context.Context, hostnames []string) ([]Route, error)
	// ReplaceEntryRoutes applies a confirmed routes plan in one transaction:
	// it checks the plan (missing, used, expired, foreign, or another
	// entry's plan, or a missing entry, is ErrNotFound), replaces the
	// entry's routes with routes and marks the plan used. A hostname and
	// path taken by another entry is ErrConflict and leaves the plan unused.
	ReplaceEntryRoutes(ctx context.Context, projectID, entryID uuid.UUID, routes []Route, confirm []byte, actor uuid.UUID, at time.Time) error

	// GetHostEdge returns the host's edge state; a host with no row is
	// {Enabled false, Status off} and no error.
	GetHostEdge(ctx context.Context, hostID uuid.UUID) (HostEdge, error)
	// SetHostEdgeEnabled sets the host's switch: enabled, and status
	// pending when enabling or when the edge is not off. It never touches
	// config_hash or applied_at. It returns the row.
	SetHostEdgeEnabled(ctx context.Context, hostID uuid.UUID, enabled bool, at time.Time) (HostEdge, error)
	// SetHostEdgeState stores a sync result: status, detail, config_hash,
	// applied_at and updated_at. It never changes enabled (a row it creates
	// is disabled).
	SetHostEdgeState(ctx context.Context, e HostEdge) error
	// ListOrgEdges returns the edge rows (enabled, or not off) of the org's
	// hosts that are not revoked.
	ListOrgEdges(ctx context.Context, orgID uuid.UUID) ([]HostEdge, error)
	// EntryHosts maps each entry of the project to the hosts where it has a
	// live (not gone) owned service row, on hosts that are not revoked.
	EntryHosts(ctx context.Context, projectID uuid.UUID) (map[string][]uuid.UUID, error)
	// HostEntries is the entries of live projects with a live owned service
	// row on the host, sorted by project id and entry.
	HostEntries(ctx context.Context, hostID uuid.UUID) ([]HostEntry, error)

	// PutEdgeConfirmation inserts one plan and drops expired ones. A
	// duplicate token hash is ErrConflict.
	PutEdgeConfirmation(ctx context.Context, hash []byte, c EdgeConfirmation) error
	GetEdgeConfirmation(ctx context.Context, hash []byte) (EdgeConfirmation, error)

	// OrgACMEEmail is the org's ACME contact, "" when unset.
	OrgACMEEmail(ctx context.Context, orgID uuid.UUID) (string, error)
	// SetOrgACMEEmail sets ("" clears) the org's ACME contact.
	SetOrgACMEEmail(ctx context.Context, orgID uuid.UUID, email string) error
}
