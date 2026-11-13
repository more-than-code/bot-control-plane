package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// EnvRefs is the env an entry resolves when it is deployed (image) or Run
// (program): env sets in order, then secret_env, then inline env
// (api-v1 v1.8 rule 12, v1.22 item 1).
type EnvRefs struct {
	Sets   []string          // env_sets, in order
	Secret map[string]string // secret_env: KEY -> "<set>.<KEY>"
	Inline map[string]string // env
}

// Any reports whether resolving needs an env set (and so the secrets key).
func (r EnvRefs) Any() bool {
	return len(r.Sets) > 0 || len(r.Secret) > 0
}

// SetNames is the sorted union of env_sets and the sets secret_env names.
func (r EnvRefs) SetNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(set string) {
		if set == "" {
			return
		}
		if _, ok := seen[set]; ok {
			return
		}
		seen[set] = struct{}{}
		out = append(out, set)
	}
	for _, set := range r.Sets {
		add(set)
	}
	for _, ref := range r.Secret {
		set, _, _ := strings.Cut(ref, ".")
		add(set)
	}
	sortStrings(out)
	return out
}

// Uses reports whether set is one of SetNames.
func (r EnvRefs) Uses(set string) bool {
	for _, name := range r.SetNames() {
		if name == set {
			return true
		}
	}
	return false
}

// PlainTakingPart is api-v1 v1.22 amendment 1: per referenced set, the plain
// keys that take part, with their values. A key takes part when its set is in
// env_sets, or when it is a secret_env target. plain holds only keys with
// is_secret false, so a secret key is never returned.
func (r EnvRefs) PlainTakingPart(plain map[string]map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	add := func(set, key, value string) {
		if out[set] == nil {
			out[set] = map[string]string{}
		}
		out[set][key] = value
	}
	for _, set := range r.Sets {
		for key, value := range plain[set] {
			add(set, key, value)
		}
	}
	for _, ref := range r.Secret {
		set, key, ok := strings.Cut(ref, ".")
		if !ok {
			continue
		}
		if value, ok := plain[set][key]; ok {
			add(set, key, value)
		}
	}
	return out
}

// EnvValueDigest is sha256 of the sorted "KEY=value" lines joined by
// newlines. An empty map hashes the empty string.
func EnvValueDigest(values map[string]string) string {
	lines := make([]string, 0, len(values))
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	sortStrings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// EnvRefs returns the image entry's env references.
func (img Image) EnvRefs() EnvRefs {
	return EnvRefs{Sets: img.EnvSets, Secret: img.SecretEnv, Inline: img.Env}
}

// EnvRefs returns the program entry's env references (api-v1 v1.22).
func (p Program) EnvRefs() EnvRefs {
	return EnvRefs{Sets: p.EnvSets, Secret: p.SecretEnv, Inline: p.Env}
}

// validateEnvSets is image rule 6: at most 8 distinct set names, each a set
// of the entry's project.
func validateEnvSets(envSets []string, sets SetKeys) error {
	if len(envSets) > maxEnvSets {
		return fieldErr("invalid env_sets")
	}
	seen := map[string]bool{}
	for _, set := range envSets {
		if !ValidName(set) || seen[set] {
			return fieldErr("invalid env_sets")
		}
		seen[set] = true
		if _, ok := sets[set]; !ok {
			return fieldErr("unknown env set")
		}
	}
	return nil
}

// mergedKeyCount is the number of distinct keys the merge would produce with
// the sets' current key names.
func mergedKeyCount(r EnvRefs, sets SetKeys) int {
	keys := map[string]struct{}{}
	for _, set := range r.Sets {
		for _, k := range sets[set] {
			keys[k] = struct{}{}
		}
	}
	for k := range r.Secret {
		keys[k] = struct{}{}
	}
	for k := range r.Inline {
		keys[k] = struct{}{}
	}
	return len(keys)
}
