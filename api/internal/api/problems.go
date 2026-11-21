package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Problems in the bell (api-v1 v1.41, spec-bell-problems §3). Detectors
// open and resolve stored rows; who sees a row is decided on each read.
const (
	// hostOfflineAfter opens host_offline; hostBackWithin resolves it.
	hostOfflineAfter = 10 * time.Minute
	hostBackWithin   = 2 * time.Minute
	// problemsGrace: the sweep does nothing this long after the API starts,
	// so agents reconnect before any host reads as quiet.
	problemsGrace = 5 * time.Minute
	// problemsHourly is the registry check and the prune. They run off the
	// minute sweep, at most one run at a time, each within
	// problemsHourlyDeadline.
	problemsHourly         = time.Hour
	problemsHourlyDeadline = 10 * time.Minute
	// registryCheckBudget caps the image-access lookups of one hourly
	// registry check (all logins together).
	registryCheckBudget = 50
	// problemWriteTimeout bounds the store calls of one detector hook.
	problemWriteTimeout = 5 * time.Second
	// problemBellWindow is how long a resolved problem stays in the bell;
	// problemRouteWindow is the same for ?resolved=1 on the problem routes.
	problemBellWindow  = 7 * 24 * time.Hour
	problemRouteWindow = 24 * time.Hour
)

// problemSweep is the sweep's own clock: when it started, when the hourly
// work last started and whether it is still running.
type problemSweep struct {
	mu         sync.Mutex
	start      time.Time
	lastHourly time.Time
	hourlyBusy bool
	// hourly counts running hourly work (tests wait on it).
	hourly sync.WaitGroup
}

// problemView is the contract Problem (api-v1 v1.41 item 1).
type problemView struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	Level      string     `json:"level"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Href       string     `json:"href"`
	OpenedAt   time.Time  `json:"opened_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	HostID     *uuid.UUID `json:"host_id"`
	ProjectID  *uuid.UUID `json:"project_id"`
	EntryID    *uuid.UUID `json:"entry_id"`
}

func (s *Server) problemsOn() bool { return !s.ProblemsOff }

// clipText cuts s to max characters.
func clipText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// problemCtx is the context of a detector's store calls: it outlives the
// caller's request but not problemWriteTimeout.
func problemCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), problemWriteTimeout)
}

// openProblem records a problem. A failure is logged with the kind only
// and never fails the caller: problems are a side channel.
func (s *Server) openProblem(ctx context.Context, in store.ProblemOpen) {
	if !s.problemsOn() {
		return
	}
	in.Title = clipText(in.Title, store.MaxProblemText)
	in.Detail = clipText(in.Detail, store.MaxProblemText)
	ctx, cancel := problemCtx(ctx)
	defer cancel()
	if _, err := s.Store.OpenProblem(ctx, in, s.now()); err != nil {
		s.logger().Error("problem open failed", "kind", in.Kind)
	}
}

// resolveProblem resolves the open problem, if any; empty title or detail
// keep the stored text.
func (s *Server) resolveProblem(ctx context.Context, kind, subject, title, detail string) {
	if !s.problemsOn() {
		return
	}
	title = clipText(title, store.MaxProblemText)
	detail = clipText(detail, store.MaxProblemText)
	ctx, cancel := problemCtx(ctx)
	defer cancel()
	if _, err := s.Store.ResolveProblem(ctx, kind, subject, title, detail, s.now()); err != nil {
		s.logger().Error("problem resolve failed", "kind", kind)
	}
}

// spanText is a short duration: "25 min", "3 h", "2 d" (at least 1 min).
func spanText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", max(1, int(d/time.Minute)))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d d", int(d/(24*time.Hour)))
	}
}

// --- host_offline -----------------------------------------------------------

// RunProblems sweeps every minute until ctx ends. It does nothing when
// BOT_API_PROBLEMS=off.
func (s *Server) RunProblems(ctx context.Context) {
	if !s.problemsOn() {
		return
	}
	s.sweep.mu.Lock()
	s.sweep.start = s.now()
	s.sweep.mu.Unlock()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepProblems(ctx)
		}
	}
}

// SweepProblems is one sweep: nothing in the first 5 minutes after start;
// then host_offline and the deploy check every call, and once an hour (in
// its own goroutine, never two at once) the registry check and the prune
// of resolved problems older than 30 days.
func (s *Server) SweepProblems(ctx context.Context) {
	if !s.problemsOn() {
		return
	}
	now := s.now()
	s.sweep.mu.Lock()
	if s.sweep.start.IsZero() {
		s.sweep.start = now
	}
	if now.Sub(s.sweep.start) < problemsGrace {
		s.sweep.mu.Unlock()
		return
	}
	hourly := !s.sweep.hourlyBusy && (s.sweep.lastHourly.IsZero() || now.Sub(s.sweep.lastHourly) >= problemsHourly)
	if hourly {
		s.sweep.lastHourly = now
		s.sweep.hourlyBusy = true
		s.sweep.hourly.Add(1)
	}
	s.sweep.mu.Unlock()
	if hourly {
		go s.hourlyProblems(ctx, now)
	}
	s.sweepHosts(ctx, now)
	s.sweepDeploys(ctx)
	s.sweepHostFiles(ctx, now)
}

// hourlyProblems is the hourly work, within problemsHourlyDeadline.
func (s *Server) hourlyProblems(ctx context.Context, now time.Time) {
	defer func() {
		s.sweep.mu.Lock()
		s.sweep.hourlyBusy = false
		s.sweep.mu.Unlock()
		s.sweep.hourly.Done()
	}()
	ctx, cancel := context.WithTimeout(ctx, problemsHourlyDeadline)
	defer cancel()
	s.checkRegistries(ctx)
	if _, err := s.Store.PruneProblems(ctx, now.Add(-store.ProblemRetention)); err != nil {
		s.logger().Error("problem prune failed")
	}
}

// sweepHosts opens host_offline for an always-on live host silent for more
// than 10 minutes and resolves it once the host reported within 2 minutes.
// A host that is revoked, archived, or no longer always on has its open
// problem resolved. A host that never reported is left alone.
func (s *Server) sweepHosts(ctx context.Context, now time.Time) {
	hosts, err := s.Store.ListProblemHosts(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostOffline)
		return
	}
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemHostOffline})
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostOffline)
		return
	}
	openBy := map[string]store.Problem{}
	for _, p := range open {
		openBy[p.Subject] = p
	}
	watched := map[string]bool{}
	for _, h := range hosts {
		if !h.AlwaysOn {
			continue
		}
		subject := h.ID.String()
		watched[subject] = true
		if h.LastSeenAt == nil {
			continue
		}
		quiet := now.Sub(*h.LastSeenAt)
		switch {
		case quiet > hostOfflineAfter:
			id := h.ID
			s.openProblem(ctx, store.ProblemOpen{
				Kind: store.ProblemHostOffline, Subject: subject, OrgID: h.OrgID, HostID: &id,
				Level: store.ProblemNeedsAction, Title: h.Name + " is offline",
				Detail: "No report for " + spanText(quiet) + ".",
			})
		case quiet < hostBackWithin:
			// No duration: opened_at does not mark when the host went quiet.
			if _, ok := openBy[subject]; ok {
				s.resolveProblem(ctx, store.ProblemHostOffline, subject, h.Name+" is back online", "Reporting again.")
			}
		}
	}
	for subject := range openBy {
		if !watched[subject] {
			s.resolveProblem(ctx, store.ProblemHostOffline, subject, "",
				"No longer checked: the host was revoked or is not always on.")
		}
	}
}

// --- registry_login ---------------------------------------------------------

func registrySubject(projectID uuid.UUID, host string) string {
	return projectID.String() + ":" + host
}

// Problem detail of a refused login (api-v1 v1.43 item 3).
const (
	detailLoginRefused       = "Token expired or revoked. Reconnect it in Settings."
	detailGitHubLoginRefused = "GitHub sign-in expired or was revoked. Reconnect GitHub in Settings."
)

// noteRegistryAccess is the registry_login hook on every image-access
// answer that used a stored login: login_failed opens, private_ok resolves.
// login is the image-access login kind (manual or github).
func (s *Server) noteRegistryAccess(ctx context.Context, projectID uuid.UUID, host, login, access string) {
	if !s.problemsOn() {
		return
	}
	subject := registrySubject(projectID, host)
	ctx, cancel := problemCtx(ctx)
	defer cancel()
	switch access {
	case accessLoginFailed:
		project, err := s.Store.GetProject(ctx, projectID)
		if err != nil {
			return
		}
		id := projectID
		detail := detailLoginRefused
		if login == loginGitHub {
			detail = detailGitHubLoginRefused
		}
		s.openProblem(ctx, store.ProblemOpen{
			Kind: store.ProblemRegistryLogin, Subject: subject, OrgID: project.OrgID, ProjectID: &id,
			Level: store.ProblemNeedsAction, Title: host + " login was refused",
			Detail: detail,
		})
	case accessPrivateOK:
		// Most answers find nothing open: read before writing.
		if s.problemOpen(ctx, store.ProblemRegistryLogin, subject) {
			s.resolveProblem(ctx, store.ProblemRegistryLogin, subject, host+" login works again", "The registry accepted the stored login.")
		}
	}
}

// problemOpen reports whether (kind, subject) has an open problem. A read
// failure answers true, so the caller still resolves.
func (s *Server) problemOpen(ctx context.Context, kind, subject string) bool {
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: kind})
	if err != nil {
		return true
	}
	for _, p := range open {
		if p.Subject == subject {
			return true
		}
	}
	return false
}

// registryLoginChanged resolves registry_login when the stored login is
// replaced (a new token or Connect GitHub) or deleted.
func (s *Server) registryLoginChanged(ctx context.Context, projectID uuid.UUID, host string, deleted bool) {
	title, detail := host+" login was replaced", "A new login was stored."
	if deleted {
		title, detail = host+" login was removed", "The stored login was deleted."
	}
	s.resolveProblem(ctx, store.ProblemRegistryLogin, registrySubject(projectID, host), title, detail)
}

// checkRegistries is the hourly registry check. For each stored login it
// looks up the project's image entries on that registry, by entry name,
// until one answers other than public (a public image says nothing about
// the login), so at most one non-public answer per login. All logins
// together use at most registryCheckBudget lookups; the rest wait for the
// next hour. An open problem resolves when its login is gone (a deleted
// project) or the project has no image entry left on that registry.
func (s *Server) checkRegistries(ctx context.Context) {
	logins, err := s.Store.ListRegistryLogins(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemRegistryLogin)
		return
	}
	stored := map[string]bool{}
	for _, l := range logins {
		stored[registrySubject(l.ProjectID, l.Host)] = true
	}
	open := map[string]bool{}
	if rows, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemRegistryLogin}); err == nil {
		for _, p := range rows {
			open[p.Subject] = true
			if !stored[p.Subject] {
				s.resolveProblem(ctx, store.ProblemRegistryLogin, p.Subject, "", "The stored login was deleted.")
			}
		}
	}
	budget := registryCheckBudget
	var entries []store.CatalogEntry
	var entriesOf uuid.UUID
	for _, l := range logins {
		if entries == nil || entriesOf != l.ProjectID {
			entries, err = s.Store.ListCatalogEntries(ctx, l.ProjectID)
			if err != nil {
				entries = nil
				continue
			}
			entriesOf = l.ProjectID
		}
		subject := registrySubject(l.ProjectID, l.Host)
		refs := registryImagesOf(entries, l.Host)
		if len(refs) == 0 {
			if open[subject] {
				s.resolveProblem(ctx, store.ProblemRegistryLogin, subject, l.Host+" login is no longer checked",
					"No catalog image uses this registry any more.")
			}
			continue
		}
		if s.Registry == nil {
			continue
		}
		for _, ref := range refs {
			if budget == 0 {
				s.logger().Warn("registry check budget reached", "kind", store.ProblemRegistryLogin)
				return
			}
			budget--
			// The empty limiter key skips the per-user limit: this check
			// is bounded by registryCheckBudget.
			got, err := s.lookupImageAccessAs(ctx, "", l.ProjectID, ref)
			if err != nil {
				s.logger().Warn("registry check skipped", "kind", store.ProblemRegistryLogin)
				break
			}
			if got.Access != accessPublic {
				break
			}
		}
	}
}

// registryImagesOf is every image entry's reference on host, in entry
// order (entries are by name).
func registryImagesOf(entries []store.CatalogEntry, host string) []catalog.ImageRef {
	var out []catalog.ImageRef
	for _, e := range entries {
		if e.Kind != "image" {
			continue
		}
		img, err := catalog.ParseStored(e.ImageRaw)
		if err != nil {
			continue
		}
		ref, err := catalog.ParseImageRef(img.Image)
		if err == nil && ref.Host == host {
			out = append(out, ref)
		}
	}
	return out
}

// --- deploy -----------------------------------------------------------------

// deployProblem is the level, title and detail per failing outcome.
type deployProblem struct {
	level, title, detail string
}

var deployProblems = map[string]deployProblem{
	"failed":            {store.ProblemHeadsUp, "%s deploy failed on %s", "The deploy did not finish. See the deploy history and logs."},
	"timeout":           {store.ProblemHeadsUp, "%s deploy timed out on %s", "The host did not answer in time."},
	"image_unavailable": {store.ProblemHeadsUp, "%s image could not be pulled on %s", "The image is missing or the registry refused the pull."},
	"forbidden_spec":    {store.ProblemHeadsUp, "%s deploy was refused on %s", "The host policy does not allow this spec."},
	"conflict":          {store.ProblemHeadsUp, "%s deploy conflicted on %s", "Another container is in the way."},
	"rolled_back":       {store.ProblemHeadsUp, "%s deploy rolled back on %s", "Unhealthy after deploy; the previous version is running."},
	"rollback_failed":   {store.ProblemNeedsAction, "%s rollback failed on %s", "The service may be down."},
	"auto_rolled_back":  {store.ProblemHeadsUp, "%s was rolled back on %s", "Unhealthy; the agent rolled back to the previous version."},
	"auto_gave_up":      {store.ProblemNeedsAction, "%s is unhealthy on %s", "The agent gave up restarting it; the service may be down."},
	"auto_failed":       {store.ProblemNeedsAction, "%s automatic recovery failed on %s", "The service may be down."},
}

func deploySubject(entryID, hostID uuid.UUID) string {
	return entryID.String() + ":" + hostID.String()
}

// DeployHook evaluates the newest finished deploy row of an entry on a
// host (after FinishImageDeploy and an agent auto action): a failing
// outcome opens or updates the deploy problem, succeeded or unchanged
// resolves it, anything else (auto_restarted) leaves it as it is.
func (s *Server) DeployHook(ctx context.Context, projectID, entryID uuid.UUID, entryName string, hostID uuid.UUID) {
	if !s.problemsOn() {
		return
	}
	ctx, cancel := problemCtx(ctx)
	defer cancel()
	rows, err := s.Store.ListImageDeploys(ctx, entryID, hostID)
	if err != nil || len(rows) == 0 {
		return
	}
	host, err := s.Store.GetHost(ctx, hostID)
	if err != nil {
		return
	}
	subject := deploySubject(entryID, hostID)
	outcome := rows[0].Outcome
	if outcome == "succeeded" || outcome == "unchanged" {
		s.resolveProblem(ctx, store.ProblemDeploy, subject, entryName+" is running on "+host.Name, "The next deploy succeeded.")
		return
	}
	d, ok := deployProblems[outcome]
	if !ok {
		return
	}
	pid, eid, hid := projectID, entryID, hostID
	s.openProblem(ctx, store.ProblemOpen{
		Kind: store.ProblemDeploy, Subject: subject, OrgID: host.OrgID,
		ProjectID: &pid, HostID: &hid, EntryID: &eid,
		Level: d.level, Title: fmt.Sprintf(d.title, entryName, host.Name), Detail: d.detail,
	})
}

// entryRemoved resolves the deploy problem when the entry's container is
// removed from the host. Deleting the entry deletes its problems with its
// deploy history (migration 0027 cascade).
func (s *Server) entryRemoved(ctx context.Context, entry store.CatalogEntry, host store.Host) {
	s.resolveProblem(ctx, store.ProblemDeploy, deploySubject(entry.ID, host.ID), entry.Name+" was removed from "+host.Name, "The container was removed.")
}

// sweepDeploys resolves open deploy problems nobody can act on any more:
// the host is revoked or archived, or the project no longer has deploy
// access to it. A read failure leaves the row for the next minute.
func (s *Server) sweepDeploys(ctx context.Context) {
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemDeploy})
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemDeploy)
		return
	}
	hosts := map[uuid.UUID]store.Host{}
	levels := map[uuid.UUID]map[uuid.UUID]string{}
	for _, p := range open {
		if p.ResolvedAt != nil || p.HostID == nil || p.ProjectID == nil {
			continue
		}
		h, ok := hosts[*p.HostID]
		if !ok {
			if h, err = s.Store.GetHost(ctx, *p.HostID); err != nil {
				continue
			}
			hosts[h.ID] = h
		}
		why := ""
		switch {
		case h.ArchivedAt != nil:
			why = "the host was removed."
		case h.RevokedAt != nil:
			why = "the host was revoked."
		default:
			byProject, ok := levels[h.ID]
			if !ok {
				rows, err := s.Store.ListHostAccess(ctx, h.ID)
				if err != nil {
					continue
				}
				byProject = map[uuid.UUID]string{}
				for _, a := range rows {
					byProject[a.ProjectID] = a.Level
				}
				levels[h.ID] = byProject
			}
			if byProject[*p.ProjectID] != authz.LevelDeploy {
				why = "the project has no deploy access to the host."
			}
		}
		if why != "" {
			s.resolveProblem(ctx, store.ProblemDeploy, p.Subject, "", "No longer checked: "+why)
		}
	}
}

// --- audience and reads -----------------------------------------------------

// problemReader decides, for one caller and one request, which problems
// they see (api-v1 v1.41 item 2) and builds their links.
type problemReader struct {
	s         *Server
	userID    uuid.UUID
	adminOrgs map[uuid.UUID]bool
	roles     map[uuid.UUID]string
	grants    map[uuid.UUID][]store.CatalogGrant
	names     map[uuid.UUID]map[uuid.UUID]string
}

func (s *Server) newProblemReader(ctx context.Context, userID uuid.UUID) (*problemReader, error) {
	orgs, err := s.Store.ListUserOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	projects, err := s.Store.ListAllProjects(ctx, userID)
	if err != nil {
		return nil, err
	}
	r := &problemReader{s: s, userID: userID, adminOrgs: map[uuid.UUID]bool{}, roles: map[uuid.UUID]string{},
		grants: map[uuid.UUID][]store.CatalogGrant{}, names: map[uuid.UUID]map[uuid.UUID]string{}}
	for _, o := range orgs {
		if store.OrgAdmin(o.Role) {
			r.adminOrgs[o.ID] = true
		}
	}
	for _, p := range projects {
		r.roles[p.ID] = p.Role
	}
	return r, nil
}

// list is the caller's problems: open ones, plus those resolved at or after
// since when it is set.
func (r *problemReader) list(ctx context.Context, since *time.Time) ([]store.Problem, error) {
	if !r.s.problemsOn() {
		return nil, nil
	}
	f := store.ProblemFilter{ResolvedSince: since}
	for id := range r.adminOrgs {
		f.OrgIDs = append(f.OrgIDs, id)
	}
	for id := range r.roles {
		f.ProjectIDs = append(f.ProjectIDs, id)
	}
	if len(f.OrgIDs) == 0 && len(f.ProjectIDs) == 0 {
		return nil, nil
	}
	rows, err := r.s.Store.ListProblems(ctx, f)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, p := range rows {
		ok, err := r.sees(ctx, p)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// sees: host_offline for org owners and admins; registry_login for project
// owners and admins (org owners and admins are project admins); deploy for
// those plus anyone holding the entry's deploy action.
func (r *problemReader) sees(ctx context.Context, p store.Problem) (bool, error) {
	if p.Kind == store.ProblemHostOffline || p.Kind == store.ProblemHostFile {
		return r.adminOrgs[p.OrgID], nil
	}
	if p.ProjectID == nil {
		return false, nil
	}
	// An owner or admin of the project's org is a project admin even when
	// they also hold a lower explicit project role.
	role := r.roles[*p.ProjectID]
	if role == authz.RoleOwner || role == authz.RoleAdmin || r.adminOrgs[p.OrgID] {
		return true, nil
	}
	if p.Kind != store.ProblemDeploy || role == "" || p.EntryID == nil {
		return false, nil
	}
	grants, ok := r.grants[*p.EntryID]
	if !ok {
		var err error
		grants, err = r.s.Store.ListCatalogGrants(ctx, *p.EntryID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		r.grants[*p.EntryID] = grants
	}
	return heldActions("image", role, r.userID, grants)["deploy"], nil
}

// href is the console page that fixes the problem.
func (r *problemReader) href(ctx context.Context, p store.Problem) string {
	switch {
	case p.Kind == store.ProblemHostFile && p.HostID != nil:
		_, name, _ := strings.Cut(p.Subject, ":")
		return hostFileHref(*p.HostID, name)
	case p.Kind == store.ProblemHostOffline && p.HostID != nil:
		return "/hosts/" + p.HostID.String()
	case p.ProjectID == nil:
		return "/projects"
	case p.Kind == store.ProblemRegistryLogin:
		return "/projects/" + p.ProjectID.String() + "?tab=settings#registries"
	}
	q := url.Values{"tab": {"catalog"}}
	if p.HostID != nil {
		q.Set("catalog.host", p.HostID.String())
	}
	if p.EntryID != nil {
		if name := r.entryName(ctx, *p.ProjectID, *p.EntryID); name != "" {
			q.Set("catalog.q", name)
		}
	}
	return "/projects/" + p.ProjectID.String() + "?" + q.Encode()
}

func (r *problemReader) entryName(ctx context.Context, projectID, entryID uuid.UUID) string {
	names, ok := r.names[projectID]
	if !ok {
		names = map[uuid.UUID]string{}
		if entries, err := r.s.Store.ListCatalogEntries(ctx, projectID); err == nil {
			for _, e := range entries {
				names[e.ID] = e.Name
			}
		}
		r.names[projectID] = names
	}
	return names[entryID]
}

func (r *problemReader) view(ctx context.Context, p store.Problem) problemView {
	return problemView{
		ID: p.ID, Kind: p.Kind, Level: p.Level, Title: p.Title, Detail: p.Detail, Href: r.href(ctx, p),
		OpenedAt: p.OpenedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(), ResolvedAt: p.ResolvedAt,
		HostID: p.HostID, ProjectID: p.ProjectID, EntryID: p.EntryID,
	}
}

// problemNotices are the caller's problems as bell items: open ones first
// (needs_action, then heads_up, newest first within), then the resolved
// ones of the last 7 days, newest first. A resolved item is always read.
func (s *Server) problemNotices(ctx context.Context, userID uuid.UUID) (open, resolved []notification, err error) {
	if !s.problemsOn() {
		return nil, nil, nil
	}
	r, err := s.newProblemReader(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	since := s.now().Add(-problemBellWindow)
	rows, err := r.list(ctx, &since)
	if err != nil {
		return nil, nil, err
	}
	sortProblems(rows)
	for _, p := range rows {
		n := notification{
			Key: "problem:" + p.ID.String(), Kind: "problem", Title: p.Title, Detail: p.Detail,
			Href: r.href(ctx, p), Level: p.Level,
		}
		if p.ResolvedAt == nil {
			n.State, n.At = "open", p.OpenedAt.UTC()
			open = append(open, n)
		} else {
			n.State, n.At = "resolved", p.ResolvedAt.UTC()
			resolved = append(resolved, n)
		}
	}
	return open, resolved, nil
}

// sortProblems is the bell order: open needs_action, open heads_up, then
// resolved; newest first within each (opened_at while open, resolved_at
// after).
func sortProblems(rows []store.Problem) {
	rank := func(p store.Problem) int {
		switch {
		case p.ResolvedAt != nil:
			return 2
		case p.Level == store.ProblemNeedsAction:
			return 0
		}
		return 1
	}
	at := func(p store.Problem) time.Time {
		if p.ResolvedAt != nil {
			return *p.ResolvedAt
		}
		return p.OpenedAt
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rank(rows[i]), rank(rows[j]); a != b {
			return a < b
		}
		return at(rows[i]).After(at(rows[j]))
	})
}

// handleOrgProblems is GET /v1/orgs/{id}/problems: org owners and admins.
func (s *Server) handleOrgProblems(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, "")
	if !ok {
		return
	}
	s.writeProblems(w, r, p, func(pr store.Problem) bool { return pr.OrgID == orgID })
}

// handleProjectProblems is GET /v1/projects/{id}/problems: the project's
// problems the caller sees in the bell.
func (s *Server) handleProjectProblems(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, "", err)
		return
	}
	s.writeProblems(w, r, p, func(pr store.Problem) bool { return pr.ProjectID != nil && *pr.ProjectID == id })
}

func (s *Server) writeProblems(w http.ResponseWriter, r *http.Request, p principal, keep func(store.Problem) bool) {
	var since *time.Time
	switch r.URL.Query().Get("resolved") {
	case "", "0":
	case "1":
		t := s.now().Add(-problemRouteWindow)
		since = &t
	default:
		writeCode(w, "bad_request", "invalid resolved")
		return
	}
	items := []problemView{}
	if s.problemsOn() {
		reader, err := s.newProblemReader(r.Context(), p.User.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		rows, err := reader.list(r.Context(), since)
		if err != nil {
			s.fail(w, err)
			return
		}
		sortProblems(rows)
		for _, pr := range rows {
			if keep(pr) {
				items = append(items, reader.view(r.Context(), pr))
			}
		}
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}
