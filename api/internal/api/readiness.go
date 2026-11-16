package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Readiness statuses and fix actions (api-v1 v1.29 item 7).
const (
	readyOK      = "ok"
	readyMissing = "missing"
	readyWarn    = "warn"
	readyUnknown = "unknown"

	fixNone          = "none"
	fixCreateEnvSet  = "create_env_set"
	fixFillValue     = "fill_value"
	fixAddFile       = "add_file"
	fixConnectGitHub = "connect_github"
	fixRegistryLogin = "add_registry_login"
	fixGrantDeploy   = "grant_deploy_access"
	fixPolicySnippet = "host_policy_snippet"
)

// readyCheck is one readiness row. Message names keys, sets, files, hosts
// and images only: never a value, a preview or a token.
type readyCheck struct {
	Check   string   `json:"check"`
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Fix     readyFix `json:"fix"`
}

// readyFix is a check's fix: the action and the names it needs. An empty
// Action is written as none.
type readyFix struct {
	Action   string     `json:"action"`
	Set      string     `json:"set,omitempty"`
	Key      string     `json:"key,omitempty"`
	Name     string     `json:"name,omitempty"`
	Registry string     `json:"registry,omitempty"`
	HostID   *uuid.UUID `json:"host_id,omitempty"`
}

type readyList struct {
	checks []readyCheck
}

func (l *readyList) add(check, status, msg string, fix readyFix) {
	if fix.Action == "" {
		fix.Action = fixNone
	}
	l.checks = append(l.checks, readyCheck{Check: check, Status: status, Message: msg, Fix: fix})
}

func (l *readyList) ready() bool {
	for _, c := range l.checks {
		if c.Status == readyMissing {
			return false
		}
	}
	return true
}

// readiness is the entry and project state the checks read.
type readiness struct {
	projectID uuid.UUID
	entry     store.CatalogEntry
	isImage   bool
	img       catalog.Image
	refs      catalog.EnvRefs
	// sets holds the material of each referenced set that exists. Values
	// are opened only for the ${KEY} check and never leave this request.
	sets map[string][]store.EnvMaterial
}

// handleReadiness is api-v1 v1.29 item 7. It is advisory: deploy keeps its
// own refusals. It contacts the registry (cached), never the host.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	var hostID *uuid.UUID
	if raw := r.URL.Query().Get("host_id"); raw != "" {
		id, ok := parseHostID(w, raw)
		if !ok {
			return
		}
		hostID = &id
	}
	entry, grants, ok := s.imageEntry(w, r, projectID, p)
	if !ok {
		return
	}
	var level string
	var host store.Host
	if hostID != nil {
		var err error
		level, err = s.Store.HostProjectAccess(r.Context(), projectID, *hostID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if level != authz.LevelDeploy {
			rel, err := authz.HostRelation(r.Context(), p.User.ID, *hostID)
			if err != nil {
				s.fail(w, err)
				return
			}
			if !rel.Sees() {
				writeCode(w, "not_found", "not found")
				return
			}
		}
		host, err = s.Store.GetHost(r.Context(), *hostID)
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if !s.readinessAllowed(r, p, projectID, entry, grants, hostID != nil && level == authz.LevelDeploy) {
		writeCode(w, "forbidden", "forbidden")
		return
	}
	rd := readiness{projectID: projectID, entry: entry, isImage: entry.Kind == "image", sets: map[string][]store.EnvMaterial{}}
	if rd.isImage {
		img, err := catalog.ParseStored(entry.ImageRaw)
		if err != nil {
			s.fail(w, err)
			return
		}
		rd.img = img
		rd.refs = img.EnvRefs()
	} else {
		rd.refs = catalog.EnvRefs{Sets: entry.Spec.EnvSets, Secret: entry.Spec.SecretEnv, Inline: entry.Spec.Env}
	}
	var list readyList
	if err := s.checkEnv(r.Context(), &rd, &list); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.checkFiles(r.Context(), rd, &list); err != nil {
		s.fail(w, err)
		return
	}
	if rd.isImage {
		if err := s.checkRegistry(r.Context(), p, rd, &list); err != nil {
			s.fail(w, err)
			return
		}
	}
	if hostID != nil {
		if err := s.checkHost(r.Context(), rd, host, level, &list); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeOK(w, http.StatusOK, map[string]any{"ready": list.ready(), "checks": list.checks})
}

// readinessAllowed: an owner or admin always; another member only with a
// host the project may deploy to and the entry's deploy action (image) or
// run action (program) granted to them.
func (s *Server) readinessAllowed(r *http.Request, p principal, projectID uuid.UUID, entry store.CatalogEntry, grants []store.CatalogGrant, deployHost bool) bool {
	role, err := s.callerRole(r.Context(), projectID, p.User.ID)
	if err != nil {
		return false
	}
	if role == authz.RoleOwner || role == authz.RoleAdmin {
		return true
	}
	action := "deploy"
	if entry.Kind != "image" {
		action = "run"
	}
	return deployHost && allowsAction(heldActions(entry.Kind, role, p.User.ID, grants), action)
}

// checkEnv adds env_sets, env_values and env_refs.
func (s *Server) checkEnv(ctx context.Context, rd *readiness, list *readyList) error {
	missingSet := false
	for _, set := range rd.refs.SetNames() {
		mats, err := s.Store.ListEnvMaterial(ctx, rd.projectID, set)
		if errors.Is(err, store.ErrNotFound) {
			missingSet = true
			list.add("env_sets", readyMissing, "env set "+set+" does not exist", readyFix{Action: fixCreateEnvSet, Set: set})
			continue
		}
		if err != nil {
			return err
		}
		rd.sets[set] = mats
	}
	if !missingSet {
		msg := "every env set the entry names exists"
		if len(rd.refs.SetNames()) == 0 {
			msg = "the entry names no env sets"
		}
		list.add("env_sets", readyOK, msg, readyFix{})
	}

	before := len(list.checks)
	reported := map[string]bool{}
	for _, set := range rd.refs.Sets {
		for _, m := range sortedMaterial(rd.sets[set]) {
			if m.NeedsValue && !reported[set+"."+m.Key] {
				reported[set+"."+m.Key] = true
				list.add("env_values", readyMissing, needsValueMsg(m.Key, set), readyFix{Action: fixFillValue, Set: set, Key: m.Key})
			}
		}
	}
	keys := make([]string, 0, len(rd.refs.Secret))
	for key := range rd.refs.Secret {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		set, name, ok := strings.Cut(rd.refs.Secret[key], ".")
		mats, exists := rd.sets[set]
		if !ok || !exists || reported[set+"."+name] {
			continue
		}
		found := false
		for _, m := range mats {
			if m.Key != name {
				continue
			}
			found = true
			if m.NeedsValue {
				reported[set+"."+name] = true
				list.add("env_values", readyMissing, needsValueMsg(name, set), readyFix{Action: fixFillValue, Set: set, Key: name})
			}
		}
		if !found {
			reported[set+"."+name] = true
			list.add("env_values", readyMissing, fmt.Sprintf("env %s in set %s does not exist", name, set), readyFix{Action: fixFillValue, Set: set, Key: name})
		}
	}
	if len(list.checks) == before {
		list.add("env_values", readyOK, "every env key the entry uses has a value", readyFix{})
	}

	if !rd.isImage {
		list.add("env_refs", readyOK, "a program takes its env literally; ${KEY} is not resolved", readyFix{})
		return nil
	}
	s.checkEnvRefs(ctx, *rd, list)
	return nil
}

// checkEnvRefs merges the env as a deploy does (mergeEnvWith, tolerant of
// what the other checks report) and resolves ${KEY} references with the
// v1.27 item 7 rules. Values stay in memory. The fix names the unknown key
// from the same error, unless the referencing value is secret (its message
// leaves the name out too).
func (s *Server) checkEnvRefs(ctx context.Context, rd readiness, list *readyList) {
	merged, err := s.mergeEnvWith(ctx, rd.projectID, rd.refs, true)
	if err != nil {
		list.add("env_refs", readyUnknown, "the env values could not be read", readyFix{})
		return
	}
	secret := make(map[string]bool, len(merged.secretKeys))
	for _, key := range merged.secretKeys {
		secret[key] = true
	}
	_, err = catalog.ResolveEnvRefs(merged.env, secret)
	var refErr *catalog.RefError
	if !errors.As(err, &refErr) {
		list.add("env_refs", readyOK, "every ${KEY} reference resolves", readyFix{})
		return
	}
	fix := readyFix{}
	if refErr.Name != "" && !secret[refErr.Key] && len(rd.refs.Sets) > 0 {
		fix = readyFix{Action: fixFillValue, Set: rd.refs.Sets[0], Key: refErr.Name}
	}
	list.add("env_refs", readyMissing, refErr.Msg, fix)
}

func sortedMaterial(mats []store.EnvMaterial) []store.EnvMaterial {
	out := append([]store.EnvMaterial(nil), mats...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// checkFiles adds files: every managed file an image entry mounts exists.
func (s *Server) checkFiles(ctx context.Context, rd readiness, list *readyList) error {
	if !rd.isImage || len(rd.img.Files) == 0 {
		list.add("files", readyOK, "the entry mounts no managed files", readyFix{})
		return nil
	}
	have, err := s.projectFileNames(ctx, rd.projectID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	missing := false
	for _, f := range rd.img.Files {
		if _, ok := have[f.File]; ok || seen[f.File] {
			continue
		}
		seen[f.File] = true
		missing = true
		list.add("files", readyMissing, "file "+f.File+" does not exist", readyFix{Action: fixAddFile, Name: f.File})
	}
	if !missing {
		list.add("files", readyOK, "every managed file the entry mounts exists", readyFix{})
	}
	return nil
}

// checkRegistry adds registry from the image-access answer (item 6).
func (s *Server) checkRegistry(ctx context.Context, p principal, rd readiness, list *readyList) error {
	ref, err := catalog.ParseImageRef(rd.img.Image)
	if err != nil {
		list.add("registry", readyWarn, "the registry could not be checked", readyFix{})
		return nil
	}
	got, err := s.lookupImageAccess(ctx, p, rd.projectID, ref)
	var limited *accessLimited
	if errors.As(err, &limited) {
		got.Access = accessUnknown
	} else if err != nil {
		return err
	}
	host := ref.Host
	loginFix := readyFix{Action: fixRegistryLogin, Registry: host}
	if host == store.GitHubRegistry {
		loginFix = readyFix{Action: fixConnectGitHub, Registry: host}
	}
	switch got.Access {
	case accessPublic:
		list.add("registry", readyOK, "public image, no login needed", readyFix{})
	case accessPrivateOK:
		msg := "private image; the stored login works"
		if got.Login == loginGitHub {
			msg = "private image; uses the GitHub connection"
		}
		list.add("registry", readyOK, msg, readyFix{})
	case accessPrivateNoLogin:
		msg := "private or missing; add a registry login"
		if host == store.GitHubRegistry {
			msg = "private or missing; connect GitHub"
		}
		list.add("registry", readyMissing, msg, loginFix)
	case accessLoginFailed:
		msg := "the stored login for " + host + " was refused; update the registry login"
		if host == store.GitHubRegistry {
			msg = "the stored login for " + host + " was refused; reconnect GitHub"
		}
		list.add("registry", readyMissing, msg, loginFix)
	case accessNotFound:
		list.add("registry", readyMissing, notFoundText(ref), readyFix{})
	default:
		list.add("registry", readyWarn, "the registry could not be checked", readyFix{})
	}
	return nil
}

// checkHost adds host_access, host_online, host_images, host_policy and
// networks. A program entry skips host_images and networks.
func (s *Server) checkHost(ctx context.Context, rd readiness, host store.Host, level string, list *readyList) error {
	if level == authz.LevelDeploy {
		list.add("host_access", readyOK, "the project has deploy access to "+host.Name, readyFix{})
	} else {
		list.add("host_access", readyMissing, reasonNoDeployAccess+"; ask an org admin to grant deploy access", readyFix{Action: fixGrantDeploy, HostID: idPtr(host.ID)})
	}
	if s.hostStatus(host) == "online" {
		list.add("host_online", readyOK, host.Name+" is online", readyFix{})
	} else {
		list.add("host_online", readyWarn, host.Name+" is offline; the deploy will wait or fail", readyFix{})
	}
	if rd.isImage {
		if containersReady(host) {
			list.add("host_images", readyOK, host.Name+" runs containers", readyFix{})
		} else {
			list.add("host_images", readyMissing, reasonNoContainers, readyFix{})
		}
	}
	if err := s.checkHostPolicy(ctx, rd, host, list); err != nil {
		return err
	}
	if !rd.isImage {
		return nil
	}
	if len(rd.img.Networks) == 0 {
		list.add("networks", readyOK, "the entry joins no shared networks", readyFix{})
		return nil
	}
	project, err := s.Store.GetProject(ctx, rd.projectID)
	if err != nil {
		return err
	}
	if msg := networksReason(host, rd.img, project.Slug); msg != "" {
		list.add("networks", readyMissing, msg+"; the host's root opens it in runner.toml", readyFix{})
	} else {
		list.add("networks", readyOK, "every shared network is open to this project", readyFix{})
	}
	return nil
}

// checkHostPolicy: the host's image allowlist is not sent to the API, so the
// check is unknown unless the entry's latest deploy to this host was
// refused policy_denied (read from the audit, where that refusal is kept).
func (s *Server) checkHostPolicy(ctx context.Context, rd readiness, host store.Host, list *readyList) error {
	if !rd.isImage {
		list.add("host_policy", readyUnknown, "the host's policy is checked when the program runs", readyFix{})
		return nil
	}
	denied, err := s.lastDeployPolicyDenied(ctx, rd.projectID, rd.entry.Name, host.ID)
	if err != nil {
		return err
	}
	if !denied {
		list.add("host_policy", readyUnknown, "the host's image allowlist is checked at deploy", readyFix{})
		return nil
	}
	pattern := rd.img.Image
	if ref, err := catalog.ParseImageRef(rd.img.Image); err == nil {
		pattern = ref.Host + "/" + ref.Path
	}
	list.add("host_policy", readyMissing, "this host does not allow image "+pattern, readyFix{Action: fixPolicySnippet, HostID: idPtr(host.ID)})
	return nil
}

// lastDeployPolicyDenied reports whether the newest catalog.deploy audit
// row for entry on host is a policy_denied refusal. A refusal stays reported
// until a later deploy of the entry to that host succeeds or is refused for
// another reason.
func (s *Server) lastDeployPolicyDenied(ctx context.Context, projectID uuid.UUID, entry string, hostID uuid.UUID) (bool, error) {
	row, err := s.Store.LatestCatalogDeploy(ctx, projectID, entry, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var detail struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(row.Detail, &detail) != nil {
		return false, nil
	}
	return row.Decision == audit.DecisionDenied && detail.Reason == agentws.PolicyDenied, nil
}

// handleHostPolicySnippet is api-v1 v1.29 item 8: text for the host's root.
// It contacts no host and holds no secret.
func (s *Server) handleHostPolicySnippet(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	hostID, ok := parseHostID(w, r.URL.Query().Get("host_id"))
	if !ok {
		return
	}
	rel, err := authz.HostRelation(r.Context(), p.User.ID, hostID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !rel.Sees() {
		level, err := s.Store.HostProjectAccess(r.Context(), projectID, hostID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if level == "" {
			writeCode(w, "not_found", "not found")
			return
		}
	}
	entry, err := s.catalogByName(r, projectID, r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if entry.Kind != "image" {
		writeCode(w, "bad_request", "kind must be image")
		return
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	ref, err := catalog.ParseImageRef(img.Image)
	if err != nil {
		s.fail(w, err)
		return
	}
	project, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, hostPolicySnippet(ref, project.Slug))
}

// hostPolicySnippet is the snippet body. The pattern is the entry's whole
// repository, never a partial wildcard (agent-protocol v1.4 refuses one).
func hostPolicySnippet(ref catalog.ImageRef, slug string) map[string]any {
	pattern := ref.Host + "/" + ref.Path
	return map[string]any{
		"image_pattern": pattern,
		"project":       slug,
		"toml":          fmt.Sprintf("images = [%q]\nprojects = [%q]\n", pattern, slug),
		"command":       fmt.Sprintf("sudo ./install.sh install --runner --project %s --image '%s'", slug, pattern),
		"note": "The host's root applies this, over SSH or with Cloud Assistant for a host without SSH: " +
			"add the pattern to images and the slug to projects in /etc/bot-agent/runner.toml, keeping the entries already there, " +
			"then restart bot-agent. The install.sh command writes a new policy and stops when runner.toml already differs.",
	}
}

// notFoundText names what the registry did not find. A 404 with a working
// login is almost always a tag or digest that does not exist, so the text
// names it, and says when the tag was implied.
func notFoundText(ref catalog.ImageRef) string {
	repo := ref.Host + "/" + ref.Path
	if ref.Digest != "" {
		return "digest " + ref.Digest + " of " + repo + " not found in the registry"
	}
	if !ref.TagGiven {
		return "tag latest of " + repo + " not found in the registry (no tag was given, so latest is used)"
	}
	return "tag " + ref.Tag + " of " + repo + " not found in the registry"
}
