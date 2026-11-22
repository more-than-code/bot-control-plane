package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Image version delete (api-v1 v1.45, spec docs/spec-image-version-delete.md):
// list a ghcr.io package's versions with their holders and stale marks, and
// delete one unheld version by id. Console sessions only; GitHub is reached
// with the project's Connect GitHub token, never echoed.

const (
	// staleAfter: an unheld version not updated for this long is stale.
	staleAfter = 14 * 24 * time.Hour
	// confirmLen is how many hex characters of the digest the delete
	// confirmation must repeat.
	confirmLen = 12
	// maxHoldersShown bounds the holders listed in a refusal.
	maxHoldersShown = 10

	msgVersionsGHCROnly   = "image versions are only available for ghcr.io images"
	msgVersionsNeedGitHub = "connect GitHub in Settings to list image versions"
	msgDeleteOrgAdmin     = "only org owners and admins can delete image versions"
	msgDeleteNoScope      = "the GitHub connection cannot delete image versions; allow it in Settings"
	msgDeleteConfirm      = "type the first 12 characters of the digest to confirm"
	msgVersionChanged     = "this version changed; reload"
)

// releaseTagRe marks a release tag (release-*, v1.2, 1.2.3).
var releaseTagRe = regexp.MustCompile(`^(release|v?[0-9]+\.[0-9]+)`)

type versionHolder struct {
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Entry       string    `json:"entry"`
	HostID      uuid.UUID `json:"host_id"`
	HostName    string    `json:"host_name"`
	Why         string    `json:"why"`
}

type versionItem struct {
	VersionID int64           `json:"version_id"`
	Digest    string          `json:"digest"`
	Tags      []string        `json:"tags"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	InUse     []versionHolder `json:"in_use"`
	Stale     bool            `json:"stale"`
	Release   bool            `json:"release"`
}

type versionsPayload struct {
	Items           []versionItem `json:"items"`
	Truncated       bool          `json:"truncated"`
	CanDelete       bool          `json:"can_delete"`
	CanDeleteReason *string       `json:"can_delete_reason"`
}

// versionsCall is the shared start of both routes once checks pass.
type versionsCall struct {
	p         principal
	projectID uuid.UUID
	orgID     uuid.UUID
	ref       catalog.ImageRef
	pkg       github.Package
	token     string
	orgAdmin  bool
	canDelete bool
	reason    string
}

func (s *Server) handleImageVersions(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	c, ok := s.startVersions(w, r, p, id, r.URL.Query().Get("image"))
	if !ok {
		return
	}
	versions, truncated, err := s.GitHub.Versions(r.Context(), c.token, c.pkg)
	if err != nil {
		s.githubPackageErr(w, c, "list", err)
		return
	}
	holders, err := s.versionHolders(r.Context(), c)
	if err != nil {
		s.fail(w, err)
		return
	}
	now := s.now()
	out := versionsPayload{Items: make([]versionItem, 0, len(versions)), Truncated: truncated, CanDelete: c.canDelete}
	if !c.canDelete {
		reason := c.reason
		out.CanDeleteReason = &reason
	}
	for _, v := range versions {
		out.Items = append(out.Items, versionView(v, holders[v.Digest], now))
	}
	s.logger().Info("image versions", "host", c.ref.Host, "path", c.ref.Path, "count", len(out.Items))
	writeOK(w, http.StatusOK, out)
}

// versionView is one listed version.
func versionView(v github.Version, held []versionHolder, now time.Time) versionItem {
	if held == nil {
		held = []versionHolder{}
	}
	item := versionItem{
		VersionID: v.ID, Digest: v.Digest, Tags: v.Tags, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		InUse: held, Release: hasReleaseTag(v.Tags),
	}
	item.Stale = len(held) == 0 && now.Sub(v.UpdatedAt) > staleAfter
	return item
}

func hasReleaseTag(tags []string) bool {
	for _, t := range tags {
		if releaseTagRe.MatchString(t) {
			return true
		}
	}
	return false
}

func (s *Server) handleImageVersionDelete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	versionID, err := strconv.ParseInt(r.PathValue("version_id"), 10, 64)
	if err != nil || versionID <= 0 {
		writeCode(w, "not_found", "not found")
		return
	}
	c, ok := s.startVersions(w, r, p, id, r.URL.Query().Get("image"))
	if !ok {
		return
	}
	if !c.orgAdmin {
		writeCode(w, "forbidden", msgDeleteOrgAdmin)
		return
	}
	if !c.canDelete {
		writeCode(w, "forbidden", c.reason)
		return
	}
	var body struct {
		Digest  string `json:"digest"`
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	v, err := s.GitHub.Version(r.Context(), c.token, c.pkg, versionID)
	if errors.Is(err, github.ErrNotFound) {
		writeCode(w, "not_found", "not found")
		return
	}
	if err != nil {
		s.githubPackageErr(w, c, "version", err)
		return
	}
	if v.Digest != body.Digest {
		writeCode(w, "conflict", msgVersionChanged)
		return
	}
	holders, err := s.versionHolders(r.Context(), c)
	if err != nil {
		s.fail(w, err)
		return
	}
	if held := holders[v.Digest]; len(held) > 0 {
		h := held[0]
		shown := held
		if len(shown) > maxHoldersShown {
			shown = shown[:maxHoldersShown]
		}
		write(w, statusFor("conflict"), "conflict",
			"in use: "+h.Entry+" on "+h.HostName+" ("+h.Why+")",
			map[string]any{"in_use": shown, "count": len(held)})
		return
	}
	if !confirmMatches(v.Digest, body.Confirm) {
		writeCode(w, "bad_request", msgDeleteConfirm)
		return
	}
	if err := s.GitHub.DeleteVersion(r.Context(), c.token, c.pkg, versionID); err != nil {
		s.githubPackageErr(w, c, "delete", err)
		return
	}
	// The project's cached tags and digests of this repository are read again.
	if s.Registry != nil {
		s.Registry.Cache.DropPrefix("tags|" + c.projectID.String() + "|" + c.ref.Host + "|" + c.ref.Path)
		s.Registry.Cache.DropPrefix("digest|" + c.projectID.String() + "|" + c.ref.Host + "|" + c.ref.Path + "|")
	}
	s.logger().Info("image version deleted", "host", c.ref.Host, "path", c.ref.Path, "version_id", versionID)
	s.auditOK(r.Context(), c.p, store.AuditRow{
		ProjectID: idPtr(c.projectID),
		Action:    audit.ActionRegistryImageVersionDeleted,
		Detail: map[string]any{
			"host": c.ref.Host, "package": c.pkg.Owner + "/" + c.pkg.Name, "version_id": versionID,
			"digest": v.Digest, "tags": v.Tags, "via": viaKind(c.p.Session.Kind),
		},
	})
	writeOK(w, http.StatusOK, map[string]any{"deleted": true, "version_id": versionID, "digest": v.Digest, "tags": v.Tags})
}

// confirmMatches: confirm is the first confirmLen hex characters of digest.
func confirmMatches(digest, confirm string) bool {
	hex := strings.TrimPrefix(digest, "sha256:")
	return len(hex) >= confirmLen && strings.ToLower(strings.TrimSpace(confirm)) == hex[:confirmLen]
}

// startVersions runs the checks both routes share after the caller passed
// the project owner-or-admin check, in order: console session, GitHub configured, a ghcr.io image with a
// Connect GitHub login, the per-user lookup limit, and the token.
func (s *Server) startVersions(w http.ResponseWriter, r *http.Request, p principal, id uuid.UUID, image string) (versionsCall, bool) {
	if !requireConsole(w, p) {
		return versionsCall{}, false
	}
	if s.GitHub == nil || s.Registry == nil {
		writeCode(w, "unavailable", msgGitHubOff)
		return versionsCall{}, false
	}
	ref, err := catalog.ParseImageRef(image)
	if err != nil {
		writeCode(w, "bad_request", "invalid image")
		return versionsCall{}, false
	}
	if ref.Host != store.GitHubRegistry {
		writeCode(w, "bad_request", msgVersionsGHCROnly)
		return versionsCall{}, false
	}
	owner, name, ok := github.SplitGHCRPath(ref.Path)
	if !ok {
		writeCode(w, "bad_request", "invalid image")
		return versionsCall{}, false
	}
	mat, err := s.Store.GetRegistryMaterial(r.Context(), id, ref.Host)
	if errors.Is(err, store.ErrNotFound) || (err == nil && mat.Source != store.RegistrySourceGitHub) {
		writeCode(w, "bad_request", msgVersionsNeedGitHub)
		return versionsCall{}, false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return versionsCall{}, false
	}
	project, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return versionsCall{}, false
	}
	orgAdmin, err := s.isOrgAdmin(r, project.OrgID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return versionsCall{}, false
	}
	c := versionsCall{p: p, projectID: id, orgID: project.OrgID, ref: ref, orgAdmin: orgAdmin}
	switch {
	case !orgAdmin:
		c.reason = msgDeleteOrgAdmin
	case !mat.DeletePackages:
		c.reason = msgDeleteNoScope
	default:
		c.canDelete = true
	}
	if ok, wait := s.Registry.Limiter.Allow(p.User.ID.String(), s.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		writeCode(w, "rate_limited", "rate limited")
		return versionsCall{}, false
	}
	_, token, err := s.registryToken(r.Context(), id, ref.Host, refreshOnUse)
	switch {
	case errors.Is(err, errSecretsKey), errors.Is(err, errDecrypt):
		s.secretWriteErr(w, err)
		return versionsCall{}, false
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "bad_request", msgVersionsNeedGitHub)
		return versionsCall{}, false
	case err != nil:
		s.writeErr(w, err, false)
		return versionsCall{}, false
	}
	c.token = token
	isOrg, err := s.GitHub.OwnerIsOrg(r.Context(), token, owner)
	if err != nil {
		s.githubPackageErr(w, c, "owner", err)
		return versionsCall{}, false
	}
	c.pkg = github.Package{Owner: owner, Org: isOrg, Name: name}
	return c, true
}

// githubPackageErr answers a failed GitHub packages call as
// registry_unavailable with a reason, logging one line without a body.
func (s *Server) githubPackageErr(w http.ResponseWriter, c versionsCall, step string, err error) {
	reason := "unreachable"
	switch {
	case errors.Is(err, github.ErrNotFound):
		reason = "not_found"
	case errors.Is(err, github.ErrDeleteRefused):
		reason = "delete_refused"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	}
	s.logger().Warn("github packages call failed", "step", step, "host", c.ref.Host, "path", c.ref.Path, "reason", reason)
	write(w, statusFor("registry_unavailable"), "registry_unavailable", "", map[string]any{"reason": reason})
}

// versionHolders maps each digest held by an image entry of this
// repository, in the org's projects the caller reaches, to its holders
// (api-v1 v1.45 §5). An org owner or admin reaches every project of the org.
func (s *Server) versionHolders(ctx context.Context, c versionsCall) (map[string][]versionHolder, error) {
	projects, err := s.Store.ListOrgProjects(ctx, c.orgID, c.p.User.ID)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	type entryRef struct {
		project uuid.UUID
		name    string
	}
	entries := map[uuid.UUID]entryRef{}
	var keys []store.EntryKey
	for _, pr := range projects {
		items, err := s.Store.ListCatalogEntries(ctx, pr.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		names[pr.ID] = pr.Name
		for _, e := range items {
			if e.Kind != "image" || !sameRepository(e.ImageRaw, c.ref) {
				continue
			}
			entries[e.ID] = entryRef{project: pr.ID, name: e.Name}
			keys = append(keys, store.EntryKey{ID: e.ID, ProjectID: pr.ID, Name: e.Name})
		}
	}
	holds, err := s.Store.ImageDigestHolds(ctx, keys)
	if err != nil {
		return nil, err
	}
	hostNames := map[uuid.UUID]string{}
	out := map[string][]versionHolder{}
	seen := map[string]bool{}
	for _, h := range holds {
		e := entries[h.EntryID]
		projectID := e.project
		hostName, ok := hostNames[h.HostID]
		if !ok {
			host, err := s.Store.GetHost(ctx, h.HostID)
			switch {
			case err == nil:
				hostName = host.Name
			case errors.Is(err, store.ErrNotFound):
				hostName = h.HostID.String()
			default:
				return nil, err
			}
			hostNames[h.HostID] = hostName
		}
		key := h.Digest + "|" + h.EntryID.String() + "|" + h.HostID.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out[h.Digest] = append(out[h.Digest], versionHolder{
			ProjectID: projectID, ProjectName: names[projectID], Entry: e.name,
			HostID: h.HostID, HostName: hostName, Why: h.Why,
		})
	}
	for d := range out {
		sort.SliceStable(out[d], func(i, j int) bool { return holdRank(out[d][i].Why) < holdRank(out[d][j].Why) })
	}
	return out, nil
}

// holdRank lists running first, then rollback targets, then recent deploys.
func holdRank(why string) int {
	switch why {
	case store.HoldRunning:
		return 0
	case store.HoldRollback:
		return 1
	default:
		return 2
	}
}

// sameRepository: the stored image of an entry names ref's registry and path.
func sameRepository(raw []byte, ref catalog.ImageRef) bool {
	img, err := catalog.ParseStored(raw)
	if err != nil {
		return false
	}
	other, err := catalog.ParseImageRef(img.Image)
	return err == nil && other.Host == ref.Host && other.Path == ref.Path
}
