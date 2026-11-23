package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/registry"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

type tagItem struct {
	Tag string `json:"tag"`
	// UpdatedAt is the tag's package version updated_at from GitHub, when
	// known (api-v1 v1.46 item 1).
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Tag orders (api-v1 v1.46 item 1): tagOrderTime when push times from GitHub
// ordered the list, tagOrderName for the v1.16 name order.
const (
	tagOrderTime = "time"
	tagOrderName = "name"
)

type tagsPayload struct {
	Items     []tagItem `json:"items"`
	Truncated bool      `json:"truncated"`
	Order     string    `json:"order"`
}

type digestPayload struct {
	Digest string `json:"digest"`
}

// registryFetch runs one upstream lookup. The bool is whether the stored
// credential was sent.
type registryFetch func(ctx context.Context, repo registry.Repo) (any, bool, error)

func (s *Server) handleImageTags(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	ref, mat, ok := s.registryImage(w, r, id, false)
	if !ok {
		return
	}
	key := "tags|" + id.String() + "|" + ref.Host + "|" + ref.Path
	v, ok := s.cachedLookup(r.Context(), w, p, id, "tags", key, ref, mat, func(ctx context.Context, repo registry.Repo) (any, bool, error) {
		list, sent, err := s.Registry.Client.Tags(ctx, repo)
		if err != nil {
			return nil, sent, err
		}
		times := s.ghcrTagTimes(ctx, ref, mat, repo)
		tags, truncated := registry.OrderByTime(list, times)
		out := tagsPayload{Items: make([]tagItem, len(tags)), Truncated: truncated, Order: tagOrderName}
		for i, t := range tags {
			out.Items[i] = tagItem{Tag: t}
			if at, ok := times[t]; ok {
				at = at.UTC()
				out.Items[i].UpdatedAt = &at
				out.Order = tagOrderTime
			}
		}
		return out, sent, nil
	})
	if ok {
		writeOK(w, http.StatusOK, v)
	}
}

// ghcrTagTimes maps each tag of a ghcr.io image to its package version's
// updated_at, read from GitHub with the project's Connect GitHub login, so
// the picker lists the newest tags first. Any other image, a manual login or
// a failed GitHub call gives nil and the plain order.
func (s *Server) ghcrTagTimes(ctx context.Context, ref catalog.ImageRef, mat *store.RegistryMaterial, repo registry.Repo) map[string]time.Time {
	if s.GitHub == nil || mat == nil || mat.Source != store.RegistrySourceGitHub || ref.Host != store.GitHubRegistry || repo.Cred == nil {
		return nil
	}
	owner, name, ok := github.SplitGHCRPath(ref.Path)
	if !ok {
		return nil
	}
	isOrg, err := s.GitHub.OwnerIsOrg(ctx, repo.Cred.Token, owner)
	if err != nil {
		s.logger().Warn("tag times unavailable", "step", "owner", "host", ref.Host, "path", ref.Path)
		return nil
	}
	versions, _, err := s.GitHub.Versions(ctx, repo.Cred.Token, github.Package{Owner: owner, Org: isOrg, Name: name})
	if err != nil {
		s.logger().Warn("tag times unavailable", "step", "versions", "host", ref.Host, "path", ref.Path)
		return nil
	}
	times := make(map[string]time.Time)
	for _, v := range versions {
		for _, t := range v.Tags {
			if prev, seen := times[t]; !seen || v.UpdatedAt.After(prev) {
				times[t] = v.UpdatedAt
			}
		}
	}
	return times
}

func (s *Server) handleImageDigest(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	ref, mat, ok := s.registryImage(w, r, id, true)
	if !ok {
		return
	}
	key := "digest|" + id.String() + "|" + ref.Host + "|" + ref.Path + "|" + ref.Tag
	v, ok := s.cachedLookup(r.Context(), w, p, id, "digest", key, ref, mat, func(ctx context.Context, repo registry.Repo) (any, bool, error) {
		digest, sent, err := s.Registry.Client.Digest(ctx, repo, ref.Tag)
		return digestPayload{Digest: digest}, sent, err
	})
	if ok {
		writeOK(w, http.StatusOK, v)
	}
}

// registryImage parses ?image= and applies the host rule. needTag refuses a
// reference without a written tag or with a digest.
func (s *Server) registryImage(w http.ResponseWriter, r *http.Request, id uuid.UUID, needTag bool) (catalog.ImageRef, *store.RegistryMaterial, bool) {
	if s.Registry == nil {
		writeCode(w, "unavailable", "registry lookups unavailable")
		return catalog.ImageRef{}, nil, false
	}
	ref, err := catalog.ParseImageRef(r.URL.Query().Get("image"))
	if err != nil || (needTag && (!ref.TagGiven || ref.Digest != "")) {
		writeCode(w, "bad_request", "invalid image")
		return catalog.ImageRef{}, nil, false
	}
	mat, err := s.Store.GetRegistryMaterial(r.Context(), id, ref.Host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeErr(w, err, false)
		return catalog.ImageRef{}, nil, false
	}
	stored := err == nil
	if !s.Registry.HostAllowed(ref.Host, stored) {
		writeCode(w, "bad_request", "registry not allowed")
		return catalog.ImageRef{}, nil, false
	}
	if !stored {
		return ref, nil, true
	}
	return ref, &mat, true
}

// cachedLookup serves key from the cache, or takes a concurrency slot and a
// rate-limit hit, opens the stored token, runs fetch, logs, and caches a
// success. On failure it has written the response.
func (s *Server) cachedLookup(ctx context.Context, w http.ResponseWriter, p principal, id uuid.UUID, kind, key string, ref catalog.ImageRef, mat *store.RegistryMaterial, fetch registryFetch) (any, bool) {
	svc := s.Registry
	if v, hit := svc.Cache.Get(key, s.now()); hit {
		s.logLookup(kind, ref, "cache", 0, "cache", nil)
		return v, true
	}
	release, ok := svc.Acquire()
	if !ok {
		w.Header().Set("Retry-After", "1")
		writeCode(w, "rate_limited", "rate limited")
		return nil, false
	}
	defer release()
	if ok, wait := svc.Limiter.Allow(p.User.ID.String(), s.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		writeCode(w, "rate_limited", "rate limited")
		return nil, false
	}
	repo := registry.Repo{Host: ref.Host, Path: ref.Path}
	if mat != nil {
		fresh, token, err := s.registryToken(ctx, id, mat.Host, refreshOnUse)
		switch {
		case errors.Is(err, errSecretsKey), errors.Is(err, errDecrypt):
			s.secretWriteErr(w, err)
			return nil, false
		case errors.Is(err, store.ErrNotFound):
			// Deleted since the read: look up without a login.
		case err != nil:
			s.writeErr(w, err, false)
			return nil, false
		default:
			repo.Cred = &registry.Credential{Username: fresh.Username, Token: token}
		}
	}
	start := time.Now()
	v, sent, err := fetch(ctx, repo)
	reason := "ok"
	var rerr *registry.Error
	if err != nil {
		reason = registry.CodeBadResponse
		if errors.As(err, &rerr) {
			reason = rerr.Code
		}
	}
	auth := "anonymous"
	if sent {
		auth = "stored"
	}
	s.logLookup(kind, ref, reason, time.Since(start), auth, rerr)
	if err != nil {
		write(w, statusFor("registry_unavailable"), "registry_unavailable", "", map[string]any{"reason": reason})
		return nil, false
	}
	svc.Cache.Put(key, v, s.now())
	return v, true
}

// logLookup never logs a token or a registry body. auth is stored,
// anonymous, or cache. refused is the registry error of a lookup that sent
// a stored credential and was refused (auth_failed), or nil; it adds the
// status, the stage and the allowlisted OCI code (spec-registry-refusal-detail).
func (s *Server) logLookup(kind string, ref catalog.ImageRef, outcome string, took time.Duration, auth string, refused *registry.Error) {
	args := []any{"kind", kind, "host", ref.Host, "path", ref.Path,
		"outcome", outcome, "ms", took.Milliseconds(), "auth", auth}
	if refused != nil && refused.Code == registry.CodeAuthFailed {
		args = append(args, "status", refused.Status, "stage", refused.Stage, "registry_code", refused.RegistryCode)
	}
	s.logger().Info("registry lookup", args...)
}
