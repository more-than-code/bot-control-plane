package api

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// programSetState is the project's sets as a program spec_hash sees them
// (api-v1 v1.22 item 2 and amendment 1): every set's key names, and the plain
// values of the sets refs uses. Secret values are never opened here.
func (s *Server) programSetState(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (catalog.SetState, error) {
	names, err := s.Store.ListEnvKeyNames(ctx, projectID)
	if err != nil {
		return catalog.SetState{}, err
	}
	plain, err := s.plainValues(ctx, projectID, refs.SetNames())
	if err != nil {
		return catalog.SetState{}, err
	}
	return catalog.SetState{Keys: names, Plain: plain}, nil
}

// plainValues reads the is_secret false values of the given sets. A set that
// no longer exists is skipped.
func (s *Server) plainValues(ctx context.Context, projectID uuid.UUID, sets []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, set := range sets {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, m := range mats {
			if m.IsSecret {
				continue
			}
			if out[set] == nil {
				out[set] = map[string]string{}
			}
			out[set][m.Key] = m.Plain
		}
	}
	return out, nil
}

// envOrigin is where a merged key's final value comes from. set is empty for
// inline env.
type envOrigin struct {
	set    string
	secret bool
}

// envOrigins follows the mergeEnv precedence without opening any value:
// env_sets in order, then secret_env, then inline env.
func (s *Server) envOrigins(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (map[string]envOrigin, error) {
	out := map[string]envOrigin{}
	for _, set := range refs.Sets {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			return nil, err
		}
		for _, m := range mats {
			out[m.Key] = envOrigin{set: set, secret: m.IsSecret}
		}
	}
	for key, ref := range refs.Secret {
		set, name, ok := strings.Cut(ref, ".")
		if !ok {
			continue
		}
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			return nil, err
		}
		for _, m := range mats {
			if m.Key == name {
				out[key] = envOrigin{set: set, secret: m.IsSecret}
				break
			}
		}
	}
	for key := range refs.Inline {
		out[key] = envOrigin{}
	}
	return out, nil
}

// originReason is the Run gate of api-v1 v1.22 amendments 2 and 3, before any
// task and before the idempotency claim. Keys are checked in name order so
// the message is stable.
func (s *Server) originReason(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (string, error) {
	origins, err := s.envOrigins(ctx, projectID, refs)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(origins))
	for key := range origins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		o := origins[key]
		if o.set == "" {
			continue
		}
		if o.secret && catalog.ChangesWhatRunsSecret(key) {
			return "this variable changes what the program runs", nil
		}
		if !o.secret && catalog.LooksSecret(key) {
			return key + " looks like a secret but is not marked secret in set " + o.set + "; mark it secret", nil
		}
	}
	return "", nil
}
