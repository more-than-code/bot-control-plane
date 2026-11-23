package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Routes (api-v1 v1.35 item 3): project owner or admin, console or
// api_delegated. A replace is always a plan confirmed with POST
// /v1/confirmations/{id}. Routes live outside the entry spec, so they
// never change spec_hash.

const (
	maxEntryRoutes     = 16
	maxNoStorePaths    = 8
	clientIPCloudflare = "cloudflare"
)

// Route states (api-v1 v1.35 item 3).
const (
	routeLive    = "live"
	routePending = "pending"
	routeError   = "error"
)

// routePathRe is "/seg[/seg…]" (1–16 segments of unreserved characters).
// Paths are lowercased first: Caddy's path matcher ignores case (review 9).
var routePathRe = regexp.MustCompile(`^(/[a-z0-9._~-]+){1,16}$`)

// noStorePathRe is a no_store_paths prefix: "/seg…" with an optional "/".
var noStorePathRe = regexp.MustCompile(`^(/[a-z0-9._~-]+){1,16}/?$`)

// dotSegment is a "." or ".." path segment, refused (review 12).
var dotSegment = regexp.MustCompile(`/\.\.?(/|$)`)

// validRoutePath is re without dot segments.
func validRoutePath(re *regexp.Regexp, p string) bool {
	return re.MatchString(p) && !dotSegment.MatchString(p)
}

func routeURL(hostname, path string) string {
	if path == "" {
		path = "/"
	}
	return "https://" + hostname + path
}

func portNotPublished(port int) string {
	return fmt.Sprintf("port %d is not published; add a ports line for it (127.0.0.1 is enough)", port)
}

// routeIn is one route of a PUT. host_id and state (read-only fields of a
// GET item) are accepted and ignored, so GET then PUT round-trips.
type routeIn struct {
	Hostname     string          `json:"hostname"`
	PathPrefix   *string         `json:"path_prefix"`
	Port         int             `json:"port"`
	ClientIPFrom *string         `json:"client_ip_from"`
	NoStorePaths []string        `json:"no_store_paths"`
	HostID       json.RawMessage `json:"host_id"`
	State        json.RawMessage `json:"state"`
}

// routeBody is a stored (normalized) route of a plan.
type routeBody struct {
	Hostname     string   `json:"hostname"`
	PathPrefix   string   `json:"path_prefix"`
	Port         int      `json:"port"`
	ClientIPFrom *string  `json:"client_ip_from"`
	NoStorePaths []string `json:"no_store_paths"`
}

type routesPlanBody struct {
	Entry  string      `json:"entry"`
	Routes []routeBody `json:"routes"`
}

func (b routeBody) route() store.Route {
	return store.Route{Hostname: b.Hostname, PathPrefix: b.PathPrefix, Port: b.Port, ClientIPFrom: b.ClientIPFrom, NoStorePaths: b.NoStorePaths}
}

func bodyOf(r store.Route) routeBody {
	paths := r.NoStorePaths
	if paths == nil {
		paths = []string{}
	}
	return routeBody{Hostname: r.Hostname, PathPrefix: r.PathPrefix, Port: r.Port, ClientIPFrom: r.ClientIPFrom, NoStorePaths: paths}
}

// normalizeRoutes checks the shape of each route and sorts them by
// hostname and path. It answers and returns false on the first problem.
func normalizeRoutes(w http.ResponseWriter, in []routeIn) ([]routeBody, bool) {
	if len(in) > maxEntryRoutes {
		writeCode(w, "bad_request", fmt.Sprintf("at most %d routes per entry", maxEntryRoutes))
		return nil, false
	}
	out := make([]routeBody, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		if !validHostname(r.Hostname) {
			writeCode(w, "bad_request", "invalid hostname: a lowercase name such as app.example.com")
			return nil, false
		}
		path := "/"
		if r.PathPrefix != nil {
			path = strings.ToLower(*r.PathPrefix)
		}
		if path != "/" && !validRoutePath(routePathRe, path) {
			writeCode(w, "bad_request", "invalid path_prefix: / or /seg[/seg…]")
			return nil, false
		}
		if r.Port < 1 || r.Port > 65535 {
			writeCode(w, "bad_request", "invalid port")
			return nil, false
		}
		if r.ClientIPFrom != nil && *r.ClientIPFrom != clientIPCloudflare {
			writeCode(w, "bad_request", "client_ip_from must be cloudflare or absent")
			return nil, false
		}
		if len(r.NoStorePaths) > maxNoStorePaths {
			writeCode(w, "bad_request", fmt.Sprintf("at most %d no_store_paths per route", maxNoStorePaths))
			return nil, false
		}
		paths := make([]string, len(r.NoStorePaths))
		for i, p := range r.NoStorePaths {
			paths[i] = strings.ToLower(p)
		}
		sort.Strings(paths)
		for i, p := range paths {
			if !validRoutePath(noStorePathRe, p) || (i > 0 && paths[i-1] == p) {
				writeCode(w, "bad_request", "invalid no_store_paths: distinct path prefixes such as /evidence/")
				return nil, false
			}
		}
		key := r.Hostname + path
		if seen[key] {
			writeCode(w, "bad_request", routeURL(r.Hostname, path)+" is listed twice")
			return nil, false
		}
		seen[key] = true
		var cip *string
		if r.ClientIPFrom != nil {
			v := *r.ClientIPFrom
			cip = &v
		}
		out = append(out, routeBody{Hostname: r.Hostname, PathPrefix: path, Port: r.Port, ClientIPFrom: cip, NoStorePaths: paths})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].PathPrefix < out[j].PathPrefix
	})
	return out, true
}

// routesFit checks the routes against the project now: domain, port and
// conflicts. A route whose key the entry already has keeps working under
// a lapsed domain; a new one needs a verified domain (spec D4).
func (s *Server) routesFit(w http.ResponseWriter, ctx context.Context, projectID uuid.UUID, entry store.CatalogEntry, routes []routeBody) bool {
	all, err := s.Store.ListProjectRoutes(ctx, projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	current := map[string]bool{}
	for _, r := range all {
		if r.EntryID == entry.ID {
			current[r.Key()] = true
		}
	}
	domains, err := s.Store.ProjectDomains(ctx, projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	hostnames := []string{}
	for _, r := range routes {
		hostnames = append(hostnames, r.Hostname)
		if _, ok := routeDomain(domains, r.Hostname, current[r.Hostname+r.PathPrefix]); !ok {
			if d, lapsed := routeDomain(domains, r.Hostname, true); lapsed {
				writeCode(w, "bad_request", fmt.Sprintf("domain %s has lapsed; verify it again before adding routes under it", d.Name))
				return false
			}
			msg := fmt.Sprintf("%s is not under a verified domain assigned to this project", r.Hostname)
			if link, ok := s.assignLink(ctx, projectID, r.Hostname); ok {
				write(w, statusFor("bad_request"), "bad_request", msg, link)
				return false
			}
			writeCode(w, "bad_request", msg)
			return false
		}
		if entry.Kind == "program" && entry.Spec.Port == nil {
			writeCode(w, "bad_request", fmt.Sprintf("program %s declares no port; add one to route it", entry.Name))
			return false
		}
		up, ok := upstreamPort(entry, r.Port)
		if ok && up < minUpstreamPort {
			writeCode(w, "bad_request", fmt.Sprintf("port %d is below %d; the edge proxies only to ports %d and up", up, minUpstreamPort, minUpstreamPort))
			return false
		}
		if !ok {
			if entry.Kind == "program" && entry.Spec.Port != nil {
				writeCode(w, "bad_request", fmt.Sprintf("port %d is not the port of program %s (%d)", r.Port, entry.Name, *entry.Spec.Port))
				return false
			}
			writeCode(w, "bad_request", portNotPublished(r.Port))
			return false
		}
	}
	taken, err := s.Store.ListRoutesByHostname(ctx, hostnames)
	if err != nil {
		s.fail(w, err)
		return false
	}
	want := map[string]routeBody{}
	for _, r := range routes {
		want[r.Hostname+r.PathPrefix] = r
	}
	for _, t := range taken {
		if r, ok := want[t.Key()]; ok && t.EntryID != entry.ID {
			writeCode(w, "conflict", routeURL(r.Hostname, r.PathPrefix)+" is already routed")
			return false
		}
	}
	return true
}

// routeEntry loads the path's catalog entry of the project.
func (s *Server) routeEntry(w http.ResponseWriter, r *http.Request, projectID uuid.UUID) (store.CatalogEntry, bool) {
	name := r.PathValue("name")
	if !catalog.ValidName(name) {
		writeCode(w, "bad_request", "invalid name")
		return store.CatalogEntry{}, false
	}
	entry, err := s.Store.GetCatalogEntry(r.Context(), projectID, name)
	if err != nil {
		s.fail(w, err)
		return store.CatalogEntry{}, false
	}
	return entry, true
}

// routeItem is one item of the route reads (api-v1 v1.35 item 3).
type routeItem struct {
	Entry        string     `json:"entry"`
	Hostname     string     `json:"hostname"`
	PathPrefix   string     `json:"path_prefix"`
	Port         int        `json:"port"`
	ClientIPFrom *string    `json:"client_ip_from"`
	NoStorePaths []string   `json:"no_store_paths"`
	HostID       *uuid.UUID `json:"host_id"`
	State        string     `json:"state"`
}

// routeItems is the routes with where they go live and their state: live
// on a host whose edge applied the current config, error when that edge
// failed, else pending (no host with the edge on runs the entry, the domain
// or port no longer fits, or the config is not applied yet).
func (s *Server) routeItems(ctx context.Context, projectID uuid.UUID, routes []store.Route) ([]routeItem, error) {
	items := []routeItem{}
	if len(routes) == 0 {
		return items, nil
	}
	entryHosts, err := s.Store.EntryHosts(ctx, projectID)
	if err != nil {
		return nil, err
	}
	type hostState struct {
		edge store.HostEdge
		hash string
		live map[string]bool
	}
	hosts := map[uuid.UUID]*hostState{}
	stateOf := func(id uuid.UUID) (*hostState, error) {
		if h, ok := hosts[id]; ok {
			return h, nil
		}
		hs := &hostState{}
		edge, err := s.Store.GetHostEdge(ctx, id)
		if err != nil {
			return nil, err
		}
		hs.edge = edge
		if edge.Enabled && edge.Status == store.EdgeApplied {
			if host, err := s.Store.GetHost(ctx, id); err == nil {
				if cfg, err := s.buildHostConfig(ctx, host); err == nil {
					hs.hash, hs.live = cfg.hash, cfg.live
				}
			}
		}
		hosts[id] = hs
		return hs, nil
	}
	for _, r := range routes {
		item := routeItem{
			Entry: r.Entry, Hostname: r.Hostname, PathPrefix: r.PathPrefix, Port: r.Port,
			ClientIPFrom: r.ClientIPFrom, NoStorePaths: bodyOf(r).NoStorePaths, State: routePending,
		}
		for _, id := range entryHosts[r.Entry] {
			hs, err := stateOf(id)
			if err != nil {
				return nil, err
			}
			if !hs.edge.Enabled {
				continue
			}
			item.HostID = idPtr(id)
			switch {
			case hs.edge.Status == store.EdgeError:
				item.State = routeError
			// Live only when the applied config serves it on this host: the
			// domain, the port and the entry's own listener checked there.
			case hs.live[r.Key()] && hs.edge.Status == store.EdgeApplied && hs.edge.ConfigHash == hs.hash:
				item.State = routeLive
			}
			break
		}
		if item.HostID == nil && len(entryHosts[r.Entry]) > 0 {
			item.HostID = idPtr(entryHosts[r.Entry][0])
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) handleProjectRoutes(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	routes, err := s.Store.ListProjectRoutes(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.routeItems(r.Context(), projectID, routes)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

// entryRoutes is the entry's routes, sorted by hostname and path.
func (s *Server) entryRoutes(ctx context.Context, projectID uuid.UUID, entryID uuid.UUID) ([]store.Route, error) {
	all, err := s.Store.ListProjectRoutes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := []store.Route{}
	for _, r := range all {
		if r.EntryID == entryID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Server) handleEntryRoutes(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	entry, ok := s.routeEntry(w, r, projectID)
	if !ok {
		return
	}
	routes, err := s.entryRoutes(r.Context(), projectID, entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.routeItems(r.Context(), projectID, routes)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

func routesRequestHash(projectID uuid.UUID, entry string, routes []routeBody) []byte {
	raw, _ := json.Marshal(routes)
	sum := sha256.Sum256([]byte(`{"action":"routes.replace","entry":` + jsonQuote(entry) +
		`,"project_id":` + jsonQuote(projectID.String()) + `,"routes":` + string(raw) + `}`))
	return sum[:]
}

// handleEntryRoutesPut is the replace plan (202) for a console or a
// delegated session.
func (s *Server) handleEntryRoutesPut(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		s.deny(w, r, p, idPtr(projectID), nil, nil, audit.ActionRoutesUpdate, err)
		return
	}
	entry, ok := s.routeEntry(w, r, projectID)
	if !ok {
		return
	}
	var body struct {
		Routes []routeIn `json:"routes"`
		Key    string    `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !catalog.ValidIdempotencyKey(body.Key) {
		writeCode(w, "bad_request", "invalid idempotency_key")
		return
	}
	routes, ok := normalizeRoutes(w, body.Routes)
	if !ok {
		return
	}
	if !s.routesFit(w, r.Context(), projectID, entry, routes) {
		return
	}
	current, err := s.entryRoutes(r.Context(), projectID, entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	plan, err := s.routesPlan(r.Context(), projectID, entry.Name, current, routes)
	if err != nil {
		s.fail(w, err)
		return
	}
	planBody, err := json.Marshal(routesPlanBody{Entry: entry.Name, Routes: routes})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.issueEdgePlan(w, r, p, store.EdgeConfirmation{
		Kind: store.EdgePlanRoutes, ProjectID: idPtr(projectID), EntryID: idPtr(entry.ID), Body: planBody,
	}, body.Key, routesRequestHash(projectID, entry.Name, routes), plan, store.AuditRow{
		ProjectID: idPtr(projectID), Action: audit.ActionRoutesConfirmation, Detail: map[string]any{"entry": entry.Name, "routes": len(routes)},
	})
}

// routeOptions is the plan text of a route's options, "" when none.
func routeOptions(r routeBody) string {
	var opts []string
	if r.ClientIPFrom != nil {
		opts = append(opts, "client IP from Cloudflare")
	}
	if len(r.NoStorePaths) > 0 {
		opts = append(opts, "no-store "+strings.Join(r.NoStorePaths, ", "))
	}
	if len(opts) == 0 {
		return ""
	}
	return " (" + strings.Join(opts, "; ") + ")"
}

// routesPlan is "routes for E: add https://H/P → port N on HOST; remove
// https://H2/; …", naming every hostname that goes live or stops.
func (s *Server) routesPlan(ctx context.Context, projectID uuid.UUID, entry string, current []store.Route, next []routeBody) (string, error) {
	where, err := s.edgeHostNames(ctx, projectID, entry)
	if err != nil {
		return "", err
	}
	old := map[string]routeBody{}
	for _, r := range current {
		old[r.Key()] = bodyOf(r)
	}
	var parts []string
	kept := map[string]bool{}
	for _, r := range next {
		key := r.Hostname + r.PathPrefix
		kept[key] = true
		line := fmt.Sprintf("%s → port %d%s%s", routeURL(r.Hostname, r.PathPrefix), r.Port, routeOptions(r), where)
		prev, had := old[key]
		switch {
		case !had:
			parts = append(parts, "add "+line)
		case !sameRoute(prev, r):
			parts = append(parts, "change "+line)
		}
	}
	for _, r := range current {
		if !kept[r.Key()] {
			parts = append(parts, "remove "+routeURL(r.Hostname, r.PathPrefix))
		}
	}
	if len(parts) == 0 {
		return "routes for " + entry + ": no change", nil
	}
	return "routes for " + entry + ":\n" + strings.Join(parts, "\n"), nil
}

func sameRoute(a, b routeBody) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// edgeHostNames is " on h1, h2" for the hosts that run the entry with the
// edge on, or the pending note.
func (s *Server) edgeHostNames(ctx context.Context, projectID uuid.UUID, entry string) (string, error) {
	hosts, err := s.Store.EntryHosts(ctx, projectID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, id := range hosts[entry] {
		edge, err := s.Store.GetHostEdge(ctx, id)
		if err != nil || !edge.Enabled {
			continue
		}
		if host, err := s.Store.GetHost(ctx, id); err == nil {
			names = append(names, host.Name)
		}
	}
	if len(names) == 0 {
		return " (pending: no host running " + entry + " has the edge on)", nil
	}
	sort.Strings(names)
	return " on " + strings.Join(names, ", "), nil
}

// issueEdgePlan stores a routes or domain delete plan and answers 202.
func (s *Server) issueEdgePlan(w http.ResponseWriter, r *http.Request, p principal, c store.EdgeConfirmation, key string, reqHash []byte, plan string, row store.AuditRow) {
	if !s.claimOrReplay(w, r, p.User.ID, key, reqHash) {
		return
	}
	plain, tokenHash, err := secret.NewToken()
	if err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	now := s.now().UTC()
	expires := now.Add(store.ConfirmationTTL)
	c.UserID, c.RequestHash, c.IdempotencyKey, c.CreatedAt, c.ExpiresAt = p.User.ID, reqHash, key, now, expires
	if err := s.Store.PutEdgeConfirmation(r.Context(), tokenHash, c); err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	out := envelopeRaw("ok", "ok", map[string]any{
		"confirmation_id": plain,
		"plan":            plan,
		"expires_at":      expires.Format(time.RFC3339),
	})
	s.finishIdempotency(r.Context(), p.User.ID, key, http.StatusAccepted, out)
	s.auditOK(r.Context(), p, row)
	writeJSON(w, http.StatusAccepted, out)
}

// confirmEdge is the last table of the confirmation chain: a routes or a
// domain delete plan (api-v1 v1.35).
func (s *Server) confirmEdge(w http.ResponseWriter, r *http.Request, p principal, hash []byte, key string) {
	c, err := s.Store.GetEdgeConfirmation(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeCode(w, "not_found", "not found")
			return
		}
		s.fail(w, err)
		return
	}
	if confirmationUnusable(w, c.UserID, p.User.ID, c.UsedAt, c.ExpiresAt, s.now()) {
		return
	}
	if key != c.IdempotencyKey {
		writeCode(w, "conflict", "idempotency_key does not match the request that issued this confirmation")
		return
	}
	lost := func() {
		s.confirmationLost(w, p, func() (uuid.UUID, *time.Time, time.Time, error) {
			c, err := s.Store.GetEdgeConfirmation(r.Context(), hash)
			return c.UserID, c.UsedAt, c.ExpiresAt, err
		})
	}
	switch c.Kind {
	case store.EdgePlanRoutes:
		s.confirmRoutes(w, r, p, hash, c, lost)
	case store.EdgePlanDomainDelete:
		s.confirmDomainDelete(w, r, p, hash, c, lost)
	default:
		writeCode(w, "not_found", "not found")
	}
}

func (s *Server) confirmRoutes(w http.ResponseWriter, r *http.Request, p principal, hash []byte, c store.EdgeConfirmation, lost func()) {
	projectID := *c.ProjectID
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		s.deny(w, r, p, idPtr(projectID), nil, nil, audit.ActionRoutesUpdate, err)
		return
	}
	var plan routesPlanBody
	if err := json.Unmarshal(c.Body, &plan); err != nil {
		s.fail(w, errors.New("routes plan body"))
		return
	}
	entry, err := s.Store.GetCatalogEntry(r.Context(), projectID, plan.Entry)
	if errors.Is(err, store.ErrNotFound) || (err == nil && entry.ID != *c.EntryID) {
		writeCode(w, "conflict", "entry "+plan.Entry+" changed since the plan; ask for a new one")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.routesFit(w, r.Context(), projectID, entry, plan.Routes) {
		return
	}
	before, err := s.entryRoutes(r.Context(), projectID, entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes := make([]store.Route, len(plan.Routes))
	for i, b := range plan.Routes {
		routes[i] = b.route()
	}
	err = s.Store.ReplaceEntryRoutes(r.Context(), projectID, entry.ID, routes, hash, p.User.ID, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		lost()
		return
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", "a hostname in the plan was routed by another entry since; ask for a new plan")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	added, removed := routeDiff(before, plan.Routes)
	s.auditOK(ctx, p, store.AuditRow{
		ProjectID: idPtr(projectID), Action: audit.ActionRoutesUpdate, Via: viaKind(p.Session.Kind),
		Detail: map[string]any{"entry": entry.Name, "added": added, "removed": removed},
	})
	s.kickEntryEdges(ctx, projectID, entry.Name)
	after, err := s.entryRoutes(ctx, projectID, entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.routeItems(ctx, projectID, after)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := envelopeRaw("ok", "ok", map[string]any{"entry": entry.Name, "routes": items})
	s.finishIdempotency(ctx, p.User.ID, c.IdempotencyKey, http.StatusOK, out)
	writeJSON(w, http.StatusOK, out)
}

// routeDiff is the URLs that go live and stop, for the audit row.
func routeDiff(before []store.Route, after []routeBody) ([]string, []string) {
	old := map[string]bool{}
	for _, r := range before {
		old[r.Key()] = true
	}
	added, removed := []string{}, []string{}
	now := map[string]bool{}
	for _, r := range after {
		now[r.Hostname+r.PathPrefix] = true
		if !old[r.Hostname+r.PathPrefix] {
			added = append(added, routeURL(r.Hostname, r.PathPrefix))
		}
	}
	for _, r := range before {
		if !now[r.Key()] {
			removed = append(removed, routeURL(r.Hostname, r.PathPrefix))
		}
	}
	return added, removed
}

// kickEntryEdges syncs the hosts that run the entry.
func (s *Server) kickEntryEdges(ctx context.Context, projectID uuid.UUID, entry string) {
	hosts, err := s.Store.EntryHosts(ctx, projectID)
	if err != nil {
		s.logger().Error("entry hosts failed")
		return
	}
	s.kickHosts(hosts[entry])
}

func (s *Server) confirmDomainDelete(w http.ResponseWriter, r *http.Request, p principal, hash []byte, c store.EdgeConfirmation, lost func()) {
	orgID := *c.OrgID
	role, err := s.Store.OrgRole(r.Context(), orgID, p.User.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	if !store.OrgAdmin(role) {
		var org *uuid.UUID
		code := "forbidden"
		if role == "" {
			code = "not_found"
		} else {
			org = idPtr(orgID)
		}
		s.denyRow(w, r, p, org, nil, nil, nil, audit.ActionDomainDelete, nil, &authz.Denied{Code: code})
		return
	}
	// A delegated session cannot hold this plan (the route is console only),
	// but the confirm route is shared: refuse it here too.
	if !requireConsole(w, p) {
		return
	}
	var body struct {
		Domain string   `json:"domain"`
		Routes []string `json:"routes"`
	}
	if err := json.Unmarshal(c.Body, &body); err != nil {
		s.fail(w, errors.New("domain plan body"))
		return
	}
	if body.Routes == nil {
		body.Routes = []string{}
	}
	removed, err := s.Store.DeleteOrgDomain(r.Context(), orgID, *c.DomainID, body.Routes, hash, p.User.ID, s.now())
	if errors.Is(err, store.ErrNotFound) {
		lost()
		return
	}
	if errors.Is(err, store.ErrRoutesChanged) {
		writeCode(w, "conflict", store.ErrRoutesChanged.Error())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	urls := []string{}
	for _, rt := range removed {
		urls = append(urls, routeURL(rt.Hostname, rt.PathPrefix))
	}
	s.auditOK(ctx, p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionDomainDelete, Detail: map[string]any{"domain": body.Domain, "routes": urls}})
	s.kickOrgEdges(ctx, orgID)
	out := envelopeRaw("ok", "ok", map[string]any{"name": body.Domain, "deleted": true, "routes_removed": len(removed)})
	s.finishIdempotency(ctx, p.User.ID, c.IdempotencyKey, http.StatusOK, out)
	writeJSON(w, http.StatusOK, out)
}

// checkRoutes is readiness check routes (api-v1 v1.35 item 4), for a
// stored entry with routes: warn with the first problem (domain lapsed or
// missing, port not published, the edge not available or off on a host
// that runs the entry or on the readiness host), else ok. Fix none.
func (s *Server) checkRoutes(ctx context.Context, rd readiness, host *store.Host, list *readyList) error {
	if rd.entry.ID == uuid.Nil {
		return nil
	}
	routes, err := s.entryRoutes(ctx, rd.projectID, rd.entry.ID)
	if err != nil || len(routes) == 0 {
		return err
	}
	warn := func(msg string) { list.add("routes", readyWarn, msg, readyFix{}) }
	domains, err := s.Store.ProjectDomains(ctx, rd.projectID)
	if err != nil {
		return err
	}
	for _, r := range routes {
		d, ok := routeDomain(domains, r.Hostname, true)
		switch {
		case !ok:
			msg := fmt.Sprintf("%s is not under a verified domain assigned to this project", r.Hostname)
			if link, ok := s.assignLink(ctx, rd.projectID, r.Hostname); ok && link.URL != "" {
				list.add("routes", readyWarn, msg, readyFix{Action: fixAssignDomain, Name: link.Name, URL: link.URL})
				return nil
			}
			warn(msg)
			return nil
		case d.Status == store.DomainLapsed:
			warn(fmt.Sprintf("domain %s has lapsed; its routes keep serving but no new route is accepted until it verifies again", d.Name))
			return nil
		}
		if rd.entry.Kind == "program" && rd.entry.Spec.Port == nil {
			warn(fmt.Sprintf("program %s declares no port; add one to route it", rd.entry.Name))
			return nil
		}
		if _, ok := upstreamPort(rd.entry, r.Port); !ok {
			warn(portNotPublished(r.Port))
			return nil
		}
	}
	entryHosts, err := s.Store.EntryHosts(ctx, rd.projectID)
	if err != nil {
		return err
	}
	ids := append([]uuid.UUID{}, entryHosts[rd.entry.Name]...)
	if host != nil {
		ids = append(ids, host.ID)
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		h, err := s.Store.GetHost(ctx, id)
		if err != nil {
			return err
		}
		if !h.Programs.EdgeAvailable() {
			warn(fmt.Sprintf("the edge is not available on %s: %s", h.Name, msgEdgeUnavailable))
			return nil
		}
		edge, err := s.Store.GetHostEdge(ctx, id)
		if err != nil {
			return err
		}
		if !edge.Enabled {
			warn(fmt.Sprintf("the edge is off on %s; an org admin turns it on in the console", h.Name))
			return nil
		}
	}
	list.add("routes", readyOK, "every route has a verified domain, a published port and the edge on", readyFix{})
	return nil
}

// domainAssignLink is the payload of a refused hostname that sits under a
// verified domain of the project's org (api-v1 v1.49): the name an org
// owner or admin would assign, written out and as a pattern, and, when the
// console origin is set, the console link that opens the domain's Assign
// dialog with this project and pattern already filled in.
type domainAssignLink struct {
	Hostname string    `json:"hostname"`
	Domain   string    `json:"domain"`
	DomainID uuid.UUID `json:"domain_id"`
	Pattern  string    `json:"pattern"`
	Name     string    `json:"name"`
	URL      string    `json:"assign_url,omitempty"`
}

// assignLink finds the most specific verified domain of the project's org
// that hostname is under. ok is false when there is none or the lookup
// fails: the refusal then stays as it was.
func (s *Server) assignLink(ctx context.Context, projectID uuid.UUID, hostname string) (domainAssignLink, bool) {
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return domainAssignLink{}, false
	}
	domains, err := s.Store.ListOrgDomains(ctx, project.OrgID)
	if err != nil {
		return domainAssignLink{}, false
	}
	var best store.OrgDomain
	for _, d := range domains {
		if d.Status == store.DomainVerified && store.UnderDomain(d.Name, hostname) && len(d.Name) > len(best.Name) {
			best = d
		}
	}
	pattern, ok := store.AssignPattern(best.Name, hostname)
	if best.Name == "" || !ok || !store.ValidPattern(best.Name, pattern) {
		return domainAssignLink{}, false
	}
	link := domainAssignLink{
		Hostname: hostname, Domain: best.Name, DomainID: best.ID, Pattern: pattern,
		Name: store.PatternName(best.Name, pattern),
	}
	if prefix, ok := consolePagePrefix(s.ConsoleOrigin, "/orgs/"); ok {
		q := url.Values{}
		q.Set("tab", "settings")
		q.Set("domains.assign", best.ID.String())
		q.Set("domains.project", projectID.String())
		q.Set("domains.pattern", pattern)
		link.URL = prefix + project.OrgID.String() + "?" + q.Encode()
	}
	return link, true
}
