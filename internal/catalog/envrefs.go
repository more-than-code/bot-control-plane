package catalog

import "strings"

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
