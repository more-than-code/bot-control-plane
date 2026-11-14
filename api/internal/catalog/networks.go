package catalog

import (
	"encoding/json"
	"regexp"
)

// maxNetworks is the api-v1 v1.25 cap on ImageSpec networks.
const maxNetworks = 4

// networkNameRe is the shared network name rule (api-v1 v1.25,
// agent-protocol v1.13).
var networkNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidNetworkName reports whether name is a valid shared network name.
func ValidNetworkName(name string) bool {
	return networkNameRe.MatchString(name)
}

// decodeNetworks reads the optional ImageSpec networks (api-v1 v1.25 item 1).
// Absent, null and [] are absent (nil). A bad name, a duplicate or more
// than 4 names is "invalid networks".
func decodeNetworks(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fieldErr("invalid networks")
	}
	if len(items) == 0 {
		return nil, nil
	}
	if err := checkNetworks(items); err != nil {
		return nil, err
	}
	return items, nil
}

// checkNetworks is the 1 to 4 unique valid names rule. ParseStored runs it
// too, so a row written outside the API cannot carry an unchecked name into
// a refusal, a plan, an audit row or a task.
func checkNetworks(items []string) error {
	if len(items) > maxNetworks {
		return fieldErr("invalid networks")
	}
	seen := make(map[string]struct{}, len(items))
	for _, name := range items {
		if !ValidNetworkName(name) {
			return fieldErr("invalid networks")
		}
		if _, dup := seen[name]; dup {
			return fieldErr("invalid networks")
		}
		seen[name] = struct{}{}
	}
	return nil
}
