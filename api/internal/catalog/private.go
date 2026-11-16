package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// Entry-private env sets (api-v1 v1.33 item 6). A private set belongs to one
// entry and is named "@<entry>". The entry's secret_env refers to its keys as
// "@<entry>.KEY"; no other entry may name it. Write bodies may give a new
// private key as {"fill": true} (a placeholder) or {"generate": {...}}
// instead of a "<set>.<KEY>" string; SplitSecretEnv turns those into refs.

// PrivatePrefix starts every private set name.
const PrivatePrefix = "@"

// PrivateSet is the private set name of entry.
func PrivateSet(entry string) string { return PrivatePrefix + entry }

// IsPrivateSet reports whether name is a private set name.
func IsPrivateSet(name string) bool {
	return strings.HasPrefix(name, PrivatePrefix) && ValidName(name[len(PrivatePrefix):])
}

// PrivateEntry is the entry name of a private set name, or "" for a shared set.
func PrivateEntry(name string) string {
	if !IsPrivateSet(name) {
		return ""
	}
	return name[len(PrivatePrefix):]
}

// ValidEnvSetName accepts a shared set name or a private set name.
func ValidEnvSetName(name string) bool {
	return ValidName(name) || IsPrivateSet(name)
}

// PrivateKey is one object form of secret_env: a placeholder (Generate nil)
// or a generated value.
type PrivateKey struct {
	Key      string
	Generate *Generate
}

// SplitSecretEnv reads a raw secret_env object whose values are either a
// "<set>.<KEY>" string or a private key object, and returns the string map
// (objects become "@<entry>.KEY") and the object keys, sorted by key.
// A null or missing object is empty. Errors name no value.
func SplitSecretEnv(entry string, raw json.RawMessage) (map[string]string, []PrivateKey, error) {
	refs := map[string]string{}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return refs, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, fieldErr("invalid secret_env")
	}
	var objects []PrivateKey
	for key, value := range fields {
		var ref string
		if err := json.Unmarshal(value, &ref); err == nil {
			refs[key] = ref
			continue
		}
		pk, err := decodePrivateKey(value)
		if err != nil {
			return nil, nil, err
		}
		pk.Key = key
		objects = append(objects, pk)
		refs[key] = PrivateSet(entry) + "." + key
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return refs, objects, nil
}

// decodePrivateKey reads {"fill": true} or {"generate": {kind, length?}}.
// Anything else, both fields, or fill false is "invalid secret_env".
func decodePrivateKey(raw json.RawMessage) (PrivateKey, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body struct {
		Fill     *bool     `json:"fill"`
		Generate *Generate `json:"generate"`
	}
	if err := dec.Decode(&body); err != nil {
		return PrivateKey{}, fieldErr("invalid secret_env")
	}
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return PrivateKey{}, fieldErr("invalid secret_env")
	}
	switch {
	case body.Fill != nil && body.Generate == nil:
		if !*body.Fill {
			return PrivateKey{}, fieldErr("invalid secret_env")
		}
		return PrivateKey{}, nil
	case body.Fill == nil && body.Generate != nil:
		if err := body.Generate.Validate(); err != nil {
			return PrivateKey{}, err
		}
		return PrivateKey{Generate: body.Generate}, nil
	default:
		return PrivateKey{}, fieldErr("invalid secret_env")
	}
}

// PrivateKeys is the sorted list of keys that secret_env takes from the
// entry's own private set.
func PrivateKeys(entry string, secretEnv map[string]string) []string {
	prefix := PrivateSet(entry) + "."
	var out []string
	for _, ref := range secretEnv {
		if strings.HasPrefix(ref, prefix) {
			out = append(out, ref[len(prefix):])
		}
	}
	sort.Strings(out)
	return out
}

// RewriteSecretEnv returns spec with its secret_env field replaced by refs.
// Other fields are kept as given. A spec that is not an object is returned
// unchanged, so the builder reports it.
func RewriteSecretEnv(spec json.RawMessage, refs map[string]string) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(spec, &fields); err != nil || fields == nil {
		return spec
	}
	if _, ok := fields["secret_env"]; !ok && len(refs) == 0 {
		return spec
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return spec
	}
	fields["secret_env"] = raw
	out, err := json.Marshal(fields)
	if err != nil {
		return spec
	}
	return out
}

// SecretEnvField returns the raw secret_env member of a spec object, or nil.
func SecretEnvField(spec json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(spec, &fields); err != nil {
		return nil
	}
	return fields["secret_env"]
}
