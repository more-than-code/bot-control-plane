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
	"github.com/more-than-code/bot-control-plane/api/internal/hostpolicy"
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
	// api-v1 v1.49: the routes check's console steps.
	fixVerifyDomain = "verify_domain"
	fixAllowEdge    = "allow_edge"
	fixTurnOnEdge   = "turn_on_edge"
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
	// ImagePattern is the repository a host_policy_snippet fix allows
	// (api-v1 v1.49).
	ImagePattern string `json:"image_pattern,omitempty"`
	// OrgID is the org whose Domains a verify_domain fix opens.
	OrgID *uuid.UUID `json:"org_id,omitempty"`
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
	// draftPrivate is, for readiness from a spec, the secret_env keys given
	// as private key objects (api-v1 v1.33 item 6), true for {"fill": true}
	// (no value until the person fills it), false for generate. They live
	// in catalog.DraftPrivateSet, which is never read from the store.
	draftPrivate map[string]bool
}

// draftPrivateMsg is the env_values row of a spec key given as
// {"fill": true}: like a stored private placeholder, it has no value yet.
func draftPrivateMsg(key string) string {
	return "env " + key + " in the entry's private set has no value yet; after the entry is created, fill it with request_env_value"
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
	host, level, ok := s.readinessHost(w, r, p, projectID, hostID)
	if !ok {
		return
	}
	if !s.readinessAllowed(r, p, projectID, entry, grants, hostID != nil && level == authz.LevelDeploy) {
		writeCode(w, "forbidden", "forbidden")
		return
	}
	rd := readiness{projectID: projectID, entry: entry, isImage: entry.Kind == "image"}
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
	s.writeReadiness(w, r, p, rd, host, level)
}

// handleReadinessSpec is api-v1 v1.32 item 3: the item 7 checks on an image
// spec that need not be stored yet. Owner or admin, console or delegated;
// it stores nothing. An invalid spec answers as a catalog create would,
// except that the env sets, keys and files it names need not exist: those
// are what the checks report.
func (s *Server) handleReadinessSpec(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	var body struct {
		Spec   json.RawMessage `json:"spec"`
		HostID *string         `json:"host_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var hostID *uuid.UUID
	if body.HostID != nil {
		id, ok := parseHostID(w, *body.HostID)
		if !ok {
			return
		}
		hostID = &id
	}
	// api-v1 v1.33 item 6: secret_env objects are judged as the write path
	// judges them, under the draft's own private set.
	spec, draftPrivate, ok := s.draftPrivateSpec(w, body.Spec)
	if !ok {
		return
	}
	names, err := s.Store.ListEnvKeyNames(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	known, err := s.projectFileNames(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if names == nil {
		names = map[string][]string{}
	}
	// A stored set of the same name (an entry called draft) is never read.
	delete(names, catalog.DraftPrivateSet)
	for key := range draftPrivate {
		names[catalog.DraftPrivateSet] = append(names[catalog.DraftPrivateSet], key)
	}
	img, err := catalog.BuildImageDraft(spec, names, known)
	if err != nil {
		s.writeField(w, err)
		return
	}
	host, level, ok := s.readinessHost(w, r, p, projectID, hostID)
	if !ok {
		return
	}
	// A spec has no entry: no name (so no deploy history for host_policy)
	// and no grants (owner or admin only, checked above).
	rd := readiness{projectID: projectID, entry: store.CatalogEntry{Kind: "image"}, isImage: true, img: img, refs: img.EnvRefs(), draftPrivate: draftPrivate}
	s.writeReadiness(w, r, p, rd, host, level)
}

// draftPrivateSpec splits the spec's secret_env (api-v1 v1.33 item 6): a
// private key object becomes a "@draft.KEY" ref, with the write path's
// checks and texts. It returns the spec to build and the object keys (true
// for fill). A "@draft.KEY" string names no stored key, as for a new entry
// on the write path ("unknown env key"). ok is false when it has answered.
func (s *Server) draftPrivateSpec(w http.ResponseWriter, raw json.RawMessage) (json.RawMessage, map[string]bool, bool) {
	field := catalog.SecretEnvField(raw)
	refs, objects, err := catalog.SplitSecretEnv(catalog.DraftEntry, field)
	if err != nil {
		s.writeField(w, err)
		return nil, nil, false
	}
	keys := map[string]bool{}
	for _, o := range objects {
		keys[o.Key] = o.Generate == nil
	}
	for key, ref := range refs {
		if _, object := keys[key]; !object && strings.HasPrefix(ref, catalog.DraftPrivateSet+".") {
			writeCode(w, "bad_request", "unknown env key")
			return nil, nil, false
		}
	}
	if len(objects) == 0 {
		return raw, keys, true
	}
	return catalog.RewriteSecretEnv(raw, refs), keys, true
}

// readinessHost loads the optional host and the project's access level on
// it. A host the project may not deploy to is visible only to a caller who
// sees it through its org; otherwise it is not_found. host is nil without
// hostID.
func (s *Server) readinessHost(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, hostID *uuid.UUID) (*store.Host, string, bool) {
	if hostID == nil {
		return nil, "", true
	}
	level, err := s.Store.HostProjectAccess(r.Context(), projectID, *hostID)
	if err != nil {
		s.fail(w, err)
		return nil, "", false
	}
	if level != authz.LevelDeploy {
		rel, err := authz.HostRelation(r.Context(), p.User.ID, *hostID)
		if err != nil {
			s.fail(w, err)
			return nil, "", false
		}
		if !rel.Sees() {
			writeCode(w, "not_found", "not found")
			return nil, "", false
		}
	}
	host, err := s.Store.GetHost(r.Context(), *hostID)
	if err != nil {
		s.fail(w, err)
		return nil, "", false
	}
	return &host, level, true
}

// writeReadiness runs the checks both readiness routes share, in the item 7
// order, and writes the Readiness object.
func (s *Server) writeReadiness(w http.ResponseWriter, r *http.Request, p principal, rd readiness, host *store.Host, level string) {
	rd.sets = map[string][]store.EnvMaterial{}
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
	if host != nil {
		if err := s.checkHost(r.Context(), rd, *host, level, &list); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := s.checkRoutes(r.Context(), rd, host, &list); err != nil {
		s.fail(w, err)
		return
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
		if rd.draftPrivate != nil && set == catalog.DraftPrivateSet {
			continue
		}
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
		if rd.draftPrivate != nil && set == catalog.DraftPrivateSet {
			if rd.draftPrivate[name] {
				list.add("env_values", readyMissing, draftPrivateMsg(name), readyFix{Action: fixFillValue, Key: name})
			}
			continue
		}
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
	} else {
		s.checkEnvRefs(ctx, *rd, list)
	}
	checkEnvPairs(*rd, list)
	return nil
}

// pairSuffixes are api-v1 v1.33 item 5: an id key and the secret key that
// belongs with it, by suffix.
var pairSuffixes = [][2]string{
	{"_CLIENT_ID", "_CLIENT_SECRET"},
	{"_KEY_ID", "_SECRET"},
	{"_ID", "_SECRET"},
}

// checkEnvPairs adds env_pairs (api-v1 v1.33 item 5): warn for each id and
// secret pair whose keys resolve from different places (inline env, a set,
// or the entry's private set), else ok. Advisory; names keys and sets only.
func checkEnvPairs(rd readiness, list *readyList) {
	names := map[string][]string{}
	for set, mats := range rd.sets {
		for _, m := range mats {
			names[set] = append(names[set], m.Key)
		}
	}
	src := envSources(rd.refs, names)
	keys := make([]string, 0, len(src))
	for key := range src {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	place := func(set string) string {
		if set == "" {
			return "the entry's env"
		}
		if rd.draftPrivate != nil && set == catalog.DraftPrivateSet {
			return "the entry's private set"
		}
		if catalog.IsPrivateSet(set) {
			return "the entry's private set " + set
		}
		return "env set " + set
	}
	seen := map[string]bool{}
	before := len(list.checks)
	for _, key := range keys {
		for _, pair := range pairSuffixes {
			stem, ok := strings.CutSuffix(key, pair[0])
			if !ok || stem == "" {
				continue
			}
			partner := stem + pair[1]
			other, ok := src[partner]
			if !ok || seen[key+"/"+partner] || other == src[key] {
				continue
			}
			seen[key+"/"+partner] = true
			list.add("env_pairs", readyWarn, fmt.Sprintf("%s comes from %s but %s comes from %s; keep a pair in one place", key, place(src[key]), partner, place(other)), readyFix{})
		}
	}
	if len(list.checks) == before {
		list.add("env_pairs", readyOK, "paired keys come from one place", readyFix{})
	}
}

// checkEnvRefs merges the env as a deploy does (mergeEnvWith, tolerant of
// what the other checks report) and resolves ${KEY} references with the
// v1.27 item 7 rules. Values stay in memory. The fix names the unknown key
// from the same error, unless the referencing value is secret (its message
// leaves the name out too).
func (s *Server) checkEnvRefs(ctx context.Context, rd readiness, list *readyList) {
	refs := rd.refs
	if len(rd.draftPrivate) > 0 {
		// The draft's private keys are not stored: merge without them and
		// add them as empty secrets, as a placeholder merges.
		refs.Secret = map[string]string{}
		for key, ref := range rd.refs.Secret {
			if !strings.HasPrefix(ref, catalog.DraftPrivateSet+".") {
				refs.Secret[key] = ref
			}
		}
	}
	merged, err := s.mergeEnvWith(ctx, rd.projectID, refs, true)
	if err != nil {
		list.add("env_refs", readyUnknown, "the env values could not be read", readyFix{})
		return
	}
	secret := make(map[string]bool, len(merged.secretKeys))
	for _, key := range merged.secretKeys {
		secret[key] = true
	}
	for key, ref := range rd.refs.Secret {
		if len(rd.draftPrivate) > 0 && strings.HasPrefix(ref, catalog.DraftPrivateSet+".") {
			if _, inline := rd.refs.Inline[key]; !inline {
				merged.env[key] = ""
				secret[key] = true
			}
		}
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
// A spec that is not stored yet has no deploy history, so it is unknown.
func (s *Server) checkHostPolicy(ctx context.Context, rd readiness, host store.Host, list *readyList) error {
	if !rd.isImage {
		list.add("host_policy", readyUnknown, "the host's policy is checked when the program runs", readyFix{})
		return nil
	}
	// api-v1 v1.49: a console-managed host sends its image patterns, so
	// the check is exact.
	if host.Programs != nil && host.Programs.ConsolePolicy {
		ref, err := catalog.ParseImageRef(rd.img.Image)
		if err != nil {
			list.add("host_policy", readyUnknown, "the host's image allowlist is checked at deploy", readyFix{})
			return nil
		}
		pattern := ref.Host + "/" + ref.Path
		if hostpolicy.Match(ref.Host, ref.Path, host.Programs.ImagePatterns) {
			list.add("host_policy", readyOK, host.Name+" allows image "+pattern, readyFix{})
			return nil
		}
		list.add("host_policy", readyMissing, "this host does not allow image "+pattern+"; an org admin allows it on the host's Policy tab", readyFix{Action: fixPolicySnippet, HostID: idPtr(host.ID), ImagePattern: pattern})
		return nil
	}
	denied := false
	if rd.entry.Name != "" {
		var err error
		if denied, err = s.lastDeployPolicyDenied(ctx, rd.projectID, rd.entry.Name, host.ID); err != nil {
			return err
		}
	}
	if !denied {
		list.add("host_policy", readyUnknown, "the host's image allowlist is checked at deploy", readyFix{})
		return nil
	}
	pattern := rd.img.Image
	if ref, err := catalog.ParseImageRef(rd.img.Image); err == nil {
		pattern = ref.Host + "/" + ref.Path
	}
	list.add("host_policy", readyMissing, "this host does not allow image "+pattern, readyFix{Action: fixPolicySnippet, HostID: idPtr(host.ID), ImagePattern: pattern})
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
