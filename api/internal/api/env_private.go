package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Entry-private secrets (api-v1 v1.33 item 6). An image or program write may
// give a secret_env key as {"fill": true} or {"generate": {...}}; the key is
// kept in the entry's private set "@<entry>" and secret_env names it as
// "@<entry>.KEY". The set is created on first use, loses the keys secret_env
// drops, and is deleted with the entry. Plans and audit rows name keys only.

// privateDraft is the private part of one entry write, before the entry is
// built: the secret_env refs, the object keys, and the private keys that
// already exist.
type privateDraft struct {
	entry    string
	refs     map[string]string
	objects  []catalog.PrivateKey
	existing []string
	given    bool
}

// draftPrivate reads secret_env (strings or private key objects) for entry
// and adds the object keys to names["@<entry>"], so the builder accepts them.
// names is changed in place.
func draftPrivate(entry string, secretRaw json.RawMessage, names map[string][]string) (privateDraft, error) {
	refs, objects, err := catalog.SplitSecretEnv(entry, secretRaw)
	if err != nil {
		return privateDraft{}, err
	}
	set := catalog.PrivateSet(entry)
	d := privateDraft{entry: entry, refs: refs, objects: objects, existing: append([]string(nil), names[set]...), given: specPresent(secretRaw)}
	union := map[string]bool{}
	for _, k := range d.existing {
		union[k] = true
	}
	for _, o := range objects {
		union[o.Key] = true
	}
	if len(union) > 0 {
		keys := make([]string, 0, len(union))
		for k := range union {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		names[set] = keys
	}
	return d, nil
}

// spec returns the image spec with secret_env as refs when the body used an
// object; otherwise the body is returned unchanged.
func (d privateDraft) spec(raw json.RawMessage) json.RawMessage {
	if len(d.objects) == 0 {
		return raw
	}
	return catalog.RewriteSecretEnv(raw, d.refs)
}

// newKeys are the object keys that are not in the private set yet. An
// object for an existing key keeps that key's value (no regenerate).
func (d privateDraft) newKeys() []catalog.PrivateKey {
	have := map[string]bool{}
	for _, k := range d.existing {
		have[k] = true
	}
	var out []catalog.PrivateKey
	for _, o := range d.objects {
		if !have[o.Key] {
			out = append(out, o)
		}
	}
	return out
}

// finish sets names["@<entry>"] to the final private keys of the built
// secret_env (so spec_hash sees what will be stored) and returns them.
func (d privateDraft) finish(secretEnv map[string]string, names map[string][]string) []string {
	keys := catalog.PrivateKeys(d.entry, secretEnv)
	set := catalog.PrivateSet(d.entry)
	if len(keys) == 0 {
		delete(names, set)
	} else {
		names[set] = keys
	}
	return keys
}

// removed are the existing private keys the write drops.
func (d privateDraft) removed(final []string) []string {
	keep := map[string]bool{}
	for _, k := range final {
		keep[k] = true
	}
	var out []string
	for _, k := range d.existing {
		if !keep[k] {
			out = append(out, k)
		}
	}
	return out
}

// planBody is the stored delegated plan body: the normalized spec with each
// new private key back in its object form, so confirm creates it.
func (d privateDraft) planBody(normalized []byte) ([]byte, error) {
	added := d.newKeys()
	if len(added) == 0 {
		return normalized, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &fields); err != nil {
		return nil, err
	}
	secret := map[string]any{}
	if raw := fields["secret_env"]; len(raw) > 0 {
		var refs map[string]string
		if err := json.Unmarshal(raw, &refs); err != nil {
			return nil, err
		}
		for k, v := range refs {
			secret[k] = v
		}
	}
	for _, k := range added {
		if k.Generate != nil {
			secret[k.Key] = map[string]any{"generate": k.Generate}
		} else {
			secret[k.Key] = map[string]any{"fill": true}
		}
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	fields["secret_env"] = raw
	return json.Marshal(fields)
}

// privateWrite seals the new keys (generated values are drawn here, so a
// plan never holds one) and returns the store write.
func (s *Server) privateWrite(projectID uuid.UUID, d privateDraft, final []string) (*store.PrivateEnv, error) {
	out := &store.PrivateEnv{Keys: final}
	set := catalog.PrivateSet(d.entry)
	keep := map[string]bool{}
	for _, k := range final {
		keep[k] = true
	}
	for _, k := range d.newKeys() {
		if !keep[k.Key] {
			continue
		}
		if s.Secrets == nil {
			return nil, errSecretsKey
		}
		plain := ""
		if k.Generate != nil {
			value, err := k.Generate.NewValue()
			if err != nil {
				return nil, err
			}
			plain = value
		}
		sealed, err := s.sealValue(projectID, set, k.Key, plain)
		if err != nil {
			return nil, err
		}
		out.Add = append(out.Add, store.EnvKeyCreate{
			Key: k.Key, Secret: true, Generated: k.Generate != nil,
			Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext, KeyVersion: sealed.KeyVersion,
		})
	}
	return out, nil
}

// privatePlanPhrase names the private keys a write adds and removes, for
// the plan sentence. Empty when there is nothing to say.
func (d privateDraft) privatePlanPhrase(final []string) string {
	keep := map[string]bool{}
	for _, k := range final {
		keep[k] = true
	}
	set := catalog.PrivateSet(d.entry)
	var parts []string
	for _, k := range d.newKeys() {
		if !keep[k.Key] {
			continue
		}
		if k.Generate == nil {
			parts = append(parts, set+"."+k.Key+" (empty; fill it with request_env_value)")
			continue
		}
		desc := "generated " + k.Generate.Kind
		if n := k.Generate.Chars(); n > 0 {
			desc += fmt.Sprintf(", %d characters", n)
		}
		parts = append(parts, set+"."+k.Key+" ("+desc+", secret)")
	}
	out := ""
	if len(parts) > 0 {
		out = "new private secrets " + strings.Join(parts, ", ")
	}
	if gone := d.removed(final); len(gone) > 0 {
		names := make([]string, len(gone))
		for i, k := range gone {
			names[i] = set + "." + k
		}
		if out != "" {
			out += "; "
		}
		out += "removes private secrets " + strings.Join(names, ", ")
	}
	return out
}

// privateKeysView is GET /catalog/{name} private_keys (api-v1 v1.33 item 6):
// names and flags only, never a value or preview.
type privateKeyView struct {
	Key           string `json:"key"`
	IsSecret      bool   `json:"is_secret"`
	NeedsValue    bool   `json:"needs_value"`
	RevealPending bool   `json:"reveal_pending"`
	// Generated (api-v1 v1.56): the value is one the API generated, so the
	// console may offer Regenerate.
	Generated bool `json:"generated"`
}

// privateKeys reads the entry's private set. A missing set is an empty list.
func (s *Server) privateKeys(ctx context.Context, projectID uuid.UUID, entry string) ([]privateKeyView, error) {
	out := []privateKeyView{}
	set, err := s.Store.GetEnvSet(ctx, projectID, catalog.PrivateSet(entry))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, nil
		}
		return nil, err
	}
	for _, k := range set.Keys {
		out = append(out, privateKeyView{Key: k.Key, IsSecret: k.IsSecret, NeedsValue: k.NeedsValue, RevealPending: k.RevealPending, Generated: k.Generated})
	}
	return out, nil
}

// keyCount is "1 key" or "n keys".
func keyCount(n int) string {
	if n == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", n)
}

// withPrivateAudit adds "private_keys" to a catalog audit detail: the
// entry's private keys after the write as "@<entry>.KEY", names only
// (api-v1 v1.33 item 6). Nothing is added when the entry has none.
func withPrivateAudit(detail map[string]any, entry string, p *store.PrivateEnv) map[string]any {
	if p == nil || len(p.Keys) == 0 {
		return detail
	}
	names := make([]string, len(p.Keys))
	for i, k := range p.Keys {
		names[i] = catalog.PrivateSet(entry) + "." + k
	}
	detail["private_keys"] = names
	return detail
}
