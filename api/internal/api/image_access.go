package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/registry"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Image access answers (api-v1 v1.29 item 6).
const (
	accessPublic         = "public"
	accessPrivateOK      = "private_ok"
	accessPrivateNoLogin = "private_no_login"
	accessLoginFailed    = "login_failed"
	accessNotFound       = "not_found"
	accessUnknown        = "unknown"

	loginNone   = "none"
	loginManual = "manual"
	loginGitHub = "github"
)

// imageAccess is the image-access payload. It never carries a token.
type imageAccess struct {
	Registry string `json:"registry"`
	Access   string `json:"access"`
	Login    string `json:"login"`
}

// accessLimited is a lookup refused by the concurrency cap or the per-user
// limit. retry is the Retry-After in seconds.
type accessLimited struct {
	retry int
}

func (e *accessLimited) Error() string { return "rate limited" }

// handleImageAccess is api-v1 v1.29 item 6: owner or admin, console or delegated.
func (s *Server) handleImageAccess(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	ref, err := catalog.ParseImageRef(r.URL.Query().Get("image"))
	if err != nil {
		writeCode(w, "bad_request", "invalid image")
		return
	}
	got, err := s.lookupImageAccess(r.Context(), p, id, ref)
	var limited *accessLimited
	if errors.As(err, &limited) {
		w.Header().Set("Retry-After", strconv.Itoa(limited.retry))
		writeCode(w, "rate_limited", "rate limited")
		return
	}
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	writeOK(w, http.StatusOK, got)
}

// lookupImageAccess decides the access of ref for the project: an anonymous
// manifest lookup, then, when that is refused and a login is stored, one
// with the login. Timeouts, a refused host and other registry errors are
// unknown. The only errors are a store error and *accessLimited. A result
// other than unknown is cached for 60 s under the project, the reference
// and the stored login, so a new login is looked up again at once.
func (s *Server) lookupImageAccess(ctx context.Context, p principal, projectID uuid.UUID, ref catalog.ImageRef) (imageAccess, error) {
	return s.lookupImageAccessAs(ctx, p.User.ID.String(), projectID, ref)
}

// lookupImageAccessAs is lookupImageAccess with the per-user limiter key; an
// empty key skips that limit (the hourly registry check, api-v1 v1.41). An
// answer that used a stored login feeds the registry_login problem.
func (s *Server) lookupImageAccessAs(ctx context.Context, limitKey string, projectID uuid.UUID, ref catalog.ImageRef) (imageAccess, error) {
	out := imageAccess{Registry: ref.Host, Access: accessUnknown, Login: loginNone}
	mat, err := s.Store.GetRegistryMaterial(ctx, projectID, ref.Host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	stored := err == nil
	loginKey := "none"
	if stored {
		out.Login = loginManual
		if mat.Source == store.RegistrySourceGitHub {
			out.Login = loginGitHub
		}
		sum := sha256.Sum256(mat.Nonce)
		loginKey = hex.EncodeToString(sum[:8])
	}
	svc := s.Registry
	if svc == nil || !svc.HostAllowed(ref.Host, stored) {
		return out, nil
	}
	reference := ref.Tag
	if ref.Digest != "" {
		reference = ref.Digest
	}
	key := "access|" + projectID.String() + "|" + ref.Host + "|" + ref.Path + "|" + reference + "|" + loginKey
	if v, hit := svc.Cache.Get(key, s.now()); hit {
		if access, ok := v.(string); ok {
			out.Access = access
			s.logLookup("access", ref, "cache", 0, "cache", nil)
			if stored {
				s.noteRegistryAccess(ctx, projectID, ref.Host, out.Login, out.Access)
			}
			return out, nil
		}
	}
	release, ok := svc.Acquire()
	if !ok {
		return out, &accessLimited{retry: 1}
	}
	defer release()
	if limitKey != "" {
		if ok, wait := svc.Limiter.Allow(limitKey, s.now()); !ok {
			return out, &accessLimited{retry: max(1, int(math.Ceil(wait.Seconds())))}
		}
	}
	start := time.Now()
	var refused *registry.Error
	out.Access, refused = s.probeAccess(ctx, projectID, ref, reference, stored, mat)
	auth := "anonymous"
	if stored && out.Access != accessPublic {
		auth = "stored"
	}
	s.logLookup("access", ref, out.Access, time.Since(start), auth, refused)
	if out.Access != accessUnknown {
		svc.Cache.Put(key, out.Access, s.now())
	}
	if stored {
		s.noteRegistryAccess(ctx, projectID, ref.Host, out.Login, out.Access)
	}
	return out, nil
}

// probeAccess runs the anonymous and then the stored-login manifest lookup.
// A 401, 403 or 404 without a credential (registry code not_found) counts
// as refused: the registry cannot tell private from missing without a login,
// so not_found is only an authenticated 404. For login_failed it also
// returns the registry error, for the lookup log; otherwise nil.
func (s *Server) probeAccess(ctx context.Context, projectID uuid.UUID, ref catalog.ImageRef, reference string, stored bool, mat store.RegistryMaterial) (string, *registry.Error) {
	repo := registry.Repo{Host: ref.Host, Path: ref.Path}
	_, _, err := s.Registry.Client.Digest(ctx, repo, reference)
	if err == nil {
		return accessPublic, nil
	}
	if registryCode(err) != registry.CodeNotFound {
		return accessUnknown, nil
	}
	if !stored {
		return accessPrivateNoLogin, nil
	}
	fresh, token, err := s.registryToken(ctx, projectID, mat.Host, refreshOnUse)
	if err != nil {
		return accessUnknown, nil
	}
	repo.Cred = &registry.Credential{Username: fresh.Username, Token: token}
	_, sent, err := s.Registry.Client.Digest(ctx, repo, reference)
	switch {
	case err == nil:
		return accessPrivateOK, nil
	case registryCode(err) == registry.CodeAuthFailed:
		var rerr *registry.Error
		errors.As(err, &rerr)
		return accessLoginFailed, rerr
	case registryCode(err) == registry.CodeNotFound && sent:
		return accessNotFound, nil
	default:
		return accessUnknown, nil
	}
}

func registryCode(err error) string {
	var rerr *registry.Error
	if errors.As(err, &rerr) {
		return rerr.Code
	}
	return registry.CodeBadResponse
}
