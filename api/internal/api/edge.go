package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/config"
	"github.com/more-than-code/bot-control-plane/api/internal/edgeconfig"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// The per-host edge (api-v1 v1.35 item 2, agent-protocol v1.15). The API
// builds the Caddy config from the host's live routes and sends it with
// edge_apply, only to a host whose hello says edge.enabled. Every change
// that can alter a host's config kicks a sync in the background; a sync
// compares the config hash with the last applied one and sends nothing
// when it is the same, except on hello, which always re-sends.

// Texts of api-v1 v1.35 item 2.
const (
	msgEdgeUnavailable = "the host's runner.toml does not allow the edge; its root adds edge = true and restarts bot-agent"
	msgEdgeOffline     = "the host is offline; the edge config is sent when it reconnects"
	msgEdgeStopOffline = "the host is offline; the edge stops when it reconnects"
	msgEdgeOutsider    = "a process outside Deploybot"
)

// Names that collide with the edge's container (deploybot-edge) and volume
// (deploybot-edge-data) names on a host (agent-protocol v1.15 item 3): a
// project slug edge, and an entry edge in a project slugged deploybot.
const (
	reservedProjectSlug = "edge"
	reservedEntrySlug   = "deploybot"
	reservedEntryName   = "edge"
	msgReservedProject  = "the project name edge is reserved for the Deploybot edge; choose another name"
	msgReservedEntry    = "the entry name edge is reserved in a project named deploybot (the Deploybot edge uses it); choose another name"
)

// reservedEntry reports whether name is the reserved entry of the project.
func (s *Server) reservedEntry(ctx context.Context, projectID uuid.UUID, name string) (bool, error) {
	if name != reservedEntryName {
		return false, nil
	}
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return false, err
	}
	return project.Slug == reservedEntrySlug, nil
}

// edgeContainer is the edge's container name (agent-protocol v1.15 item 3).
// Its own listeners never count as occupants of 80 and 443.
const edgeContainer = "deploybot-edge"

// edgeSyncTimeout bounds one background sync, including an image pull.
const edgeSyncTimeout = time.Duration(agentws.EdgeTimeoutSec+30) * time.Second

// edgeSync runs at most one background sync per host. A kick while that
// host's sync runs marks it dirty, and the running goroutine syncs once
// more when it ends (review 10). wg lets tests wait for every sync.
type edgeSync struct {
	mu    sync.Mutex
	hosts map[uuid.UUID]*hostSync
	wg    sync.WaitGroup
}

type hostSync struct {
	dirty bool
	force bool
}

// EdgeHook is the agentws hook: a hello re-sends the full config. A host
// whose edge was never on is skipped without a goroutine.
func (s *Server) EdgeHook(hostID uuid.UUID, hello bool) {
	edge, err := s.Store.GetHostEdge(context.Background(), hostID)
	if err != nil || (!edge.Enabled && edge.Status == store.EdgeOff && edge.ConfigHash == "") {
		return
	}
	s.kickEdge(hostID, hello)
}

// kickEdge syncs one host in the background, coalescing with a running sync.
func (s *Server) kickEdge(hostID uuid.UUID, force bool) {
	s.edge.mu.Lock()
	if s.edge.hosts == nil {
		s.edge.hosts = map[uuid.UUID]*hostSync{}
	}
	if st, running := s.edge.hosts[hostID]; running {
		st.dirty = true
		st.force = st.force || force
		s.edge.mu.Unlock()
		return
	}
	s.edge.hosts[hostID] = &hostSync{}
	s.edge.wg.Add(1)
	s.edge.mu.Unlock()
	go func() {
		defer s.edge.wg.Done()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), edgeSyncTimeout)
			s.syncEdge(ctx, hostID, force)
			cancel()
			s.edge.mu.Lock()
			st := s.edge.hosts[hostID]
			if st.dirty {
				force = st.force
				st.dirty, st.force = false, false
				s.edge.mu.Unlock()
				continue
			}
			delete(s.edge.hosts, hostID)
			s.edge.mu.Unlock()
			return
		}
	}()
}

// kickOrgEdges syncs every host of the org whose edge is on (or stopping).
func (s *Server) kickOrgEdges(ctx context.Context, orgID uuid.UUID) {
	edges, err := s.Store.ListOrgEdges(ctx, orgID)
	if err != nil {
		s.logger().Error("edge list failed")
		return
	}
	for _, e := range edges {
		s.kickEdge(e.HostID, false)
	}
}

// kickHosts syncs each host once.
func (s *Server) kickHosts(hosts []uuid.UUID) {
	seen := map[uuid.UUID]bool{}
	for _, h := range hosts {
		if !seen[h] {
			seen[h] = true
			s.kickEdge(h, false)
		}
	}
}

// waitEdges waits for the background syncs started so far (tests).
func (s *Server) waitEdges() { s.edge.wg.Wait() }

// msgEdgePolicyOff is the detail after the host's runner.toml turned the
// edge off (review B2): the API stopped it.
const msgEdgePolicyOff = "the host's runner.toml no longer allows the edge"

// syncEdge brings one host's edge to its stored switch and routes. Only
// kickEdge calls it, one goroutine per host. It writes the result with
// SetHostEdgeState, which never touches the switch, so a disable made
// during a long apply survives and the next (dirty) sync stops the edge.
func (s *Server) syncEdge(ctx context.Context, hostID uuid.UUID, force bool) {
	edge, err := s.Store.GetHostEdge(ctx, hostID)
	if err != nil || (!edge.Enabled && edge.Status == store.EdgeOff && edge.ConfigHash == "") {
		return
	}
	host, err := s.Store.GetHost(ctx, hostID)
	if err != nil || host.RevokedAt != nil {
		return
	}
	online := s.Hub != nil && s.hostStatus(host) == "online"
	available := host.Programs.EdgeAvailable()
	// A 0.2.11+ agent reports programs.edge; enabled false means the host's
	// root turned it off. Older agents omit it and are never sent a task.
	policyOff := host.Programs != nil && host.Programs.Edge != nil && !host.Programs.Edge.Enabled
	running := edge.ConfigHash != "" || edge.Status == store.EdgeApplied
	next := edge
	stop := func(offDetail string) {
		if _, err := s.Hub.Edge(ctx, host, "edge_stop", map[string]any{}); err != nil {
			next.Status, next.Detail = store.EdgeError, edgeErrDetail(err)
			return
		}
		next.Status, next.Detail, next.ConfigHash, next.AppliedAt = store.EdgeOff, offDetail, "", nil
	}
	switch {
	case policyOff && running && online:
		// The agent allows edge_stop whatever its policy says.
		stop(msgEdgePolicyOff)
		s.saveEdge(ctx, next)
		return
	case policyOff && running:
		next.Status, next.Detail = store.EdgePending, msgEdgeStopOffline
		s.saveEdge(ctx, next)
		return
	case policyOff && !running:
		next.Status, next.Detail = store.EdgeOff, msgEdgePolicyOff
		if edge.Status != next.Status || edge.Detail != next.Detail {
			s.saveEdge(ctx, next)
		}
		return
	}
	if !edge.Enabled {
		switch {
		case !available && !policyOff:
			// An older agent (or no policy): no task can be sent.
			next.Status, next.Detail = store.EdgeOff, ""
		case !online:
			next.Status, next.Detail = store.EdgePending, msgEdgeStopOffline
		default:
			stop("")
		}
		s.saveEdge(ctx, next)
		return
	}
	cfg, err := s.buildHostConfig(ctx, host)
	switch {
	case err != nil:
		next.Status, next.Detail = store.EdgeError, "the edge config could not be built"
		if errors.Is(err, edgeconfig.ErrTooLarge) {
			next.Detail = edgeconfig.ErrTooLarge.Error()
		}
	case !available:
		next.Status, next.Detail = store.EdgePending, msgEdgeUnavailable
	case !force && edge.Status == store.EdgeApplied && edge.ConfigHash == cfg.hash:
		return
	case !online:
		next.Status, next.Detail = store.EdgePending, msgEdgeOffline
	default:
		res, err := s.Hub.Edge(ctx, host, "edge_apply", edgeApplySpec(cfg))
		if err == nil {
			err = appliedHash(res.Payload, cfg.hash)
		}
		if err != nil {
			next.Status, next.Detail = store.EdgeError, edgeErrDetail(err)
		} else {
			at := s.now()
			next.Status, next.Detail, next.ConfigHash, next.AppliedAt = store.EdgeApplied, "", cfg.hash, &at
		}
	}
	s.saveEdge(ctx, next)
}

// edgeApplySpec is the edge_apply task spec: the config bytes as built (the
// hash is of exactly these bytes) and their hash.
func edgeApplySpec(cfg hostConfig) map[string]any {
	return map[string]any{
		"config":      json.RawMessage(cfg.raw),
		"config_hash": cfg.hash,
	}
}

// saveEdge stores a sync result. It never writes enabled.
func (s *Server) saveEdge(ctx context.Context, e store.HostEdge) {
	e.UpdatedAt = s.now()
	if err := s.Store.SetHostEdgeState(ctx, e); err != nil {
		s.logger().Error("edge state update failed", "host_id", e.HostID.String())
	}
}

// appliedHash checks the agent's answer names the config it was sent.
func appliedHash(payload json.RawMessage, want string) error {
	var body struct {
		ConfigHash string `json:"config_hash"`
	}
	if json.Unmarshal(payload, &body) == nil && body.ConfigHash != "" && body.ConfigHash != want {
		return errors.New("the host applied a different config")
	}
	return nil
}

// edgeAgentErrors is the sentence for each agent error code of an edge
// task (agent-protocol v1.15 item 4, review C1).
var edgeAgentErrors = map[string]string{
	"edge_disabled":      "the host's runner.toml does not allow the edge",
	"port_in_use":        "port 80 or 443 is in use on the host",
	"invalid_config":     "Caddy refused the config; the previous config keeps serving",
	"conflict":           "a container or volume named deploybot-edge exists that Deploybot does not own",
	"bad_spec":           "the agent refused the edge task as malformed",
	"docker_unavailable": "Docker is not available on the host",
	"image_unavailable":  "the edge image could not be pulled",
	"io_error":           "the agent could not write the edge files",
	"helper_unavailable": "the agent's root helper is not available",
}

// edgeErrDetail is the host_edges detail for a failed edge task: the
// sentence for the agent's code, plus the agent's detail when it adds
// something. A port_in_use detail that names the port replaces the
// sentence instead of repeating it.
func edgeErrDetail(err error) string {
	var ce *agentws.CallError
	if !errors.As(err, &ce) {
		return clipDetail(err.Error())
	}
	detail := agentDetail(ce.Payload)
	msg, known := edgeAgentErrors[ce.Agent]
	switch {
	case ce.Agent == "":
		// A timeout or an unavailable host: the API's own message.
		msg = ce.Msg
	case !known:
		msg = "the agent refused the edge task (" + ce.Agent + ")"
	case ce.Agent == "port_in_use" && strings.Contains(detail, "port"):
		return clipDetail(detail)
	}
	if detail != "" && detail != msg && detail != ce.Agent {
		msg += ": " + detail
	}
	return clipDetail(msg)
}

// agentDetail is the agent's reason in a failed edge result.
func agentDetail(payload json.RawMessage) string {
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return ""
	}
	for _, key := range []string{"detail", "holder", "message", "error"} {
		if v, ok := body[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clipDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// hostConfig is a built edge config. live is the route keys it serves.
type hostConfig struct {
	raw    []byte
	hash   string
	routes int
	live   map[string]bool
}

// buildHostConfig is the Caddy config for the host's live routes: routes of
// entries with a live owned service on the host, under a verified or lapsed
// domain assigned to the route's project, whose upstream port is one of
// that entry's own listeners on the host (review A1).
func (s *Server) buildHostConfig(ctx context.Context, host store.Host) (hostConfig, error) {
	entries, err := s.Store.HostEntries(ctx, host.ID)
	if err != nil {
		return hostConfig{}, err
	}
	byProject := map[uuid.UUID]map[string][]int{}
	var projects []uuid.UUID
	for _, e := range entries {
		if byProject[e.ProjectID] == nil {
			byProject[e.ProjectID] = map[string][]int{}
			projects = append(projects, e.ProjectID)
		}
		byProject[e.ProjectID][e.Entry] = e.Ports
	}
	var routes []edgeconfig.Route
	live := map[string]bool{}
	for _, projectID := range projects {
		got, err := s.liveRoutes(ctx, projectID, byProject[projectID])
		if err != nil {
			return hostConfig{}, err
		}
		for _, r := range got {
			live[r.Hostname+r.PathPrefix] = true
		}
		routes = append(routes, got...)
	}
	email, err := s.Store.OrgACMEEmail(ctx, host.OrgID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return hostConfig{}, err
	}
	raw, hash, err := edgeconfig.Build(routes, edgeconfig.Options{ACMEEmail: email, InternalIssuer: s.EdgeTLSIssuer == config.EdgeIssuerInternal})
	if err != nil {
		return hostConfig{}, err
	}
	return hostConfig{raw: raw, hash: hash, routes: len(routes), live: live}, nil
}

// liveRoutes is the project's routes of the given entries that may serve;
// entries maps each entry to its listeners on the host.
func (s *Server) liveRoutes(ctx context.Context, projectID uuid.UUID, entries map[string][]int) ([]edgeconfig.Route, error) {
	all, err := s.Store.ListProjectRoutes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var mine []store.Route
	for _, r := range all {
		if _, ok := entries[r.Entry]; ok {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	domains, err := s.Store.ProjectDomains(ctx, projectID)
	if err != nil {
		return nil, err
	}
	specs, err := s.Store.ListCatalogEntries(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byName := map[string]store.CatalogEntry{}
	for _, e := range specs {
		byName[e.Name] = e
	}
	var out []edgeconfig.Route
	for _, r := range mine {
		if _, ok := routeDomain(domains, r.Hostname, true); !ok {
			continue
		}
		up, ok := upstreamPort(byName[r.Entry], r.Port)
		if !ok || up < minUpstreamPort || !slices.Contains(entries[r.Entry], up) {
			// The spec changed since the deploy (or names a port the entry
			// does not listen on): never proxy to another listener.
			continue
		}
		out = append(out, edgeconfig.Route{
			Hostname: r.Hostname, PathPrefix: r.PathPrefix, Upstream: up,
			CloudflareClientIP: r.ClientIPFrom != nil && *r.ClientIPFrom == clientIPCloudflare,
			NoStorePaths:       r.NoStorePaths,
		})
	}
	return out, nil
}

// minUpstreamPort is the lowest port the edge proxies to (the agent's
// allowlist: 127.0.0.1:1024-65535).
const minUpstreamPort = 1024

// routeDomain finds the domain that lets the project route hostname: a
// verified domain, or with keepLapsed also a lapsed one, with an
// assignment covering the hostname. domains is ProjectDomains output.
func routeDomain(domains []store.OrgDomain, hostname string, keepLapsed bool) (store.OrgDomain, bool) {
	for _, d := range domains {
		if d.Status != store.DomainVerified && !(keepLapsed && d.Status == store.DomainLapsed) {
			continue
		}
		for _, a := range d.Assignments {
			if store.Covers(d.Name, a.Pattern, hostname) {
				return d, true
			}
		}
	}
	return store.OrgDomain{}, false
}

// upstreamPort resolves a route port: an image entry's container port to
// the lowest host port that publishes it; a program's declared port as is.
// A program without a declared port has no upstream.
func upstreamPort(entry store.CatalogEntry, port int) (int, bool) {
	switch entry.Kind {
	case "image":
		img, err := catalog.ParseStored(entry.ImageRaw)
		if err != nil {
			return 0, false
		}
		ports := append([]catalog.Port(nil), img.Ports...)
		sort.Slice(ports, func(i, j int) bool { return ports[i].Host < ports[j].Host })
		for _, p := range ports {
			if p.Container == port {
				return p.Host, true
			}
		}
		return 0, false
	case "program":
		if entry.Spec.Port == nil || *entry.Spec.Port != port {
			return 0, false
		}
		return port, true
	default:
		return 0, false
	}
}

// edgeView is GET /v1/hosts/{id}/edge (api-v1 v1.35 item 2).
type edgeView struct {
	Available  bool       `json:"available"`
	Enabled    bool       `json:"enabled"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail"`
	ConfigHash string     `json:"config_hash"`
	AppliedAt  *time.Time `json:"applied_at"`
	Routes     int        `json:"routes"`
}

func (s *Server) edgeView(ctx context.Context, host store.Host) (edgeView, error) {
	edge, err := s.Store.GetHostEdge(ctx, host.ID)
	if err != nil {
		return edgeView{}, err
	}
	view := edgeView{
		Available: host.Programs.EdgeAvailable(), Enabled: edge.Enabled, Status: edge.Status,
		Detail: edge.Detail, ConfigHash: edge.ConfigHash, AppliedAt: edge.AppliedAt,
	}
	if edge.Enabled {
		if cfg, err := s.buildHostConfig(ctx, host); err == nil {
			view.Routes = cfg.routes
		}
	}
	return view, nil
}

func (s *Server) handleEdgeGet(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, ""); !ok {
		return
	}
	view, err := s.edgeView(r.Context(), host)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, view)
}

func (s *Server) handleEdgePut(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeCode(w, "bad_request", "enabled is required")
		return
	}
	action := audit.ActionEdgeEnable
	if !*body.Enabled {
		action = audit.ActionEdgeDisable
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, action); !ok {
		return
	}
	if host.RevokedAt != nil {
		writeCode(w, "conflict", "the host is revoked")
		return
	}
	if *body.Enabled {
		if !host.Programs.EdgeAvailable() {
			writeCode(w, "bad_request", msgEdgeUnavailable)
			return
		}
		occupant, err := s.edgeOccupant(r.Context(), host)
		if err != nil {
			s.fail(w, err)
			return
		}
		if occupant != "" {
			writeCode(w, "conflict", occupant)
			return
		}
	}
	// Only the switch is written here; a sync writes only the state, so a
	// running apply cannot undo this (review B2).
	edge, err := s.Store.SetHostEdgeEnabled(r.Context(), host.ID, *body.Enabled, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, hostAuditRow(host, nil, action))
	// Enabling re-sends the full config even if it was applied before.
	s.kickEdge(host.ID, edge.Enabled)
	view, err := s.edgeView(r.Context(), host)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, view)
}

// edgeOccupant names what holds 80 or 443 on the host, from discovery, or
// "" when nothing but the edge does (api-v1 v1.35 item 2).
func (s *Server) edgeOccupant(ctx context.Context, host store.Host) (string, error) {
	services, err := s.Store.ListServices(ctx, host.ID, nil, s.now())
	if err != nil {
		return "", err
	}
	for _, port := range []int{443, 80} {
		for _, svc := range services {
			if svc.GoneAt != nil || !slices.Contains(svc.Ports, port) || isEdgeService(svc) {
				continue
			}
			holder := msgEdgeOutsider
			if svc.ProjectID != nil && svc.CatalogEntry != nil {
				project, err := s.Store.GetProject(ctx, *svc.ProjectID)
				if err == nil {
					holder = fmt.Sprintf("%s (project %s)", *svc.CatalogEntry, project.Name)
				}
			}
			return fmt.Sprintf("port %d is used by %s", port, holder), nil
		}
	}
	return "", nil
}

// isEdgeService is the edge's own discovered row (owner kind edge, agent-
// protocol v1.15 item 5): it has no project and is the edge container.
func isEdgeService(svc store.Service) bool {
	if svc.ProjectID != nil {
		return false
	}
	return svc.Name == edgeContainer || (svc.ContainerName != nil && *svc.ContainerName == edgeContainer)
}

// withEdge sets the host read's edge summary (api-v1 v1.35).
func (s *Server) withEdge(ctx context.Context, h store.Host) (store.Host, error) {
	edge, err := s.Store.GetHostEdge(ctx, h.ID)
	if err != nil {
		return h, err
	}
	h.Edge = &store.HostEdgeView{Enabled: edge.Enabled, Status: edge.Status}
	return h, nil
}
