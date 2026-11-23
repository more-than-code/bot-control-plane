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

// NetworkHost is the only ImageSpec network value (api-v1 v1.50 item 1,
// agent-protocol v1.21 item 3): the container shares the host's network.
const NetworkHost = "host"

// decodeNetwork reads the optional ImageSpec network. Absent and null are
// "". Any value but "host" is "invalid network", and so is "host" with
// shared networks.
func decodeNetwork(fields map[string]json.RawMessage, networks []string) (string, error) {
	network, set, err := decodeOptionalString(fields, "network")
	if err != nil || (set && network == "") {
		return "", fieldErr("invalid network")
	}
	if err := checkNetwork(network, networks); err != nil {
		return "", err
	}
	return network, nil
}

// checkNetwork is the network rule. ParseStored runs it too.
func checkNetwork(network string, networks []string) error {
	if network == "" {
		return nil
	}
	if network != NetworkHost {
		return fieldErr("invalid network")
	}
	if len(networks) > 0 {
		return fieldErr("network host cannot join shared networks")
	}
	return nil
}
