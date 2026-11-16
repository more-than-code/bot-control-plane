package catalog

import (
	"encoding/json"
	"strings"
)

// draftName stands in for the entry name when a spec is checked before the
// entry exists. It only has to pass ValidImageName.
const draftName = "draft"

// BuildImageDraft validates an image spec as BuildImage does for a create,
// with the same error texts, except that the env sets, set keys and managed
// files the spec names need not exist yet: readiness from a spec (api-v1
// v1.32 item 3) reports those as checks instead. sets and files are the
// project's current names and are not changed.
func BuildImageDraft(raw []byte, sets SetKeys, files map[string]struct{}) (Image, error) {
	fields, err := specFields(raw)
	if err != nil {
		return Image{}, err
	}
	draftSets := make(SetKeys, len(sets))
	for set, keys := range sets {
		draftSets[set] = append([]string(nil), keys...)
	}
	draftFiles := make(map[string]struct{}, len(files))
	for name := range files {
		draftFiles[name] = struct{}{}
	}
	// Each lookup below is lenient: a field that does not decode is left to
	// BuildImage, which refuses it with the create text.
	if names, err := decodeNameList(fields, "env_sets"); err == nil {
		for _, set := range names {
			if _, ok := draftSets[set]; !ok {
				draftSets[set] = []string{}
			}
		}
	}
	if secretEnv, err := decodeStringMap(fields, "secret_env"); err == nil {
		for _, ref := range secretEnv {
			if set, key, ok := strings.Cut(ref, "."); ok {
				draftSets[set] = append(draftSets[set], key)
			}
		}
	}
	var mounts []fileIn
	if raw, ok := fields["files"]; ok && json.Unmarshal(raw, &mounts) == nil {
		for _, m := range mounts {
			draftFiles[m.File] = struct{}{}
		}
	}
	return BuildImage(draftName, raw, draftSets, draftFiles)
}
