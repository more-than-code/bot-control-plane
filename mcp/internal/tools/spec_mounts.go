package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Bounds and rules for spec.ports, spec.volumes and spec.files, matching
// api-v1 ImageSpec rules 4, 8 and 13. The API checks them again and stays the
// authority; these checks exist so an agent learns the field and the accepted
// shape instead of the API's bare "invalid ports". No message echoes a value.
const (
	portsMax       = 16
	volumesMax     = 8
	filesMax       = 16
	mountTargetMax = 255

	portsShape   = `each port is {"host": 1-65535, "container": 1-65535, "public": false}; public false (the default) binds 127.0.0.1 on the host, public true binds 0.0.0.0 and needs allow_public_ports in the host's runner.toml`
	volumesShape = `each volume is {"name": "data", "target": "/var/lib/app", "read_only": false}; name is lowercase letters, digits and -, at most 48 characters (the host volume is <project>-<name>); target is an absolute path in the container`
	filesShape   = `each file is {"file": "<project file name>", "target": "/etc/app/config.yaml"}, mounted read-only; the file must already exist in the project (request_file adds one)`
)

var (
	volumeNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	specFileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// decodeSpecList decodes a list field into raw items. ok is false for null.
func decodeSpecList(raw json.RawMessage, field, shape string, max int) ([]json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, false, fmt.Errorf("%s must be an array: %s", field, shape)
	}
	if len(items) > max {
		return nil, false, fmt.Errorf("%s allows at most %d items", field, max)
	}
	return items, true, nil
}

// decodeSpecItem decodes one list item as an object and refuses keys outside
// allowed. An unknown key is named only when it is a plain [a-z_] word.
func decodeSpecItem(raw json.RawMessage, label, shape string, allowed ...string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		kind := "a non-object value"
		if len(trimmed) > 0 && trimmed[0] == '"' {
			kind = "a string"
		}
		return nil, fmt.Errorf("%s is %s; %s", label, kind, shape)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("%s must be an object: %s", label, shape)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must be an object: %s", label, shape)
	}
	for key := range fields {
		known := false
		for _, a := range allowed {
			if key == a {
				known = true
				break
			}
		}
		if known {
			continue
		}
		if safePolicyKey(key) {
			return nil, fmt.Errorf("%s.%s is not a field: %s", label, key, shape)
		}
		return nil, fmt.Errorf("%s has an unknown field: %s", label, shape)
	}
	return fields, nil
}

func specInt(raw json.RawMessage, min, max int) (int, bool) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err != nil {
		return 0, false
	}
	v, err := n.Int64()
	if err != nil || v < int64(min) || v > int64(max) {
		return 0, false
	}
	return int(v), true
}

func specBool(raw json.RawMessage) bool {
	var b bool
	return json.Unmarshal(raw, &b) == nil
}

func specString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// validatePorts is ImageSpec rule 4 with the accepted shape in each message.
func validatePorts(raw json.RawMessage) error {
	items, _, err := decodeSpecList(raw, "ports", portsShape, portsMax)
	if err != nil {
		return err
	}
	hosts := map[int]struct{}{}
	for i, item := range items {
		label := fmt.Sprintf("ports[%d]", i)
		fields, err := decodeSpecItem(item, label, portsShape, "host", "container", "public")
		if err != nil {
			return err
		}
		host, hasHost := fields["host"]
		container, hasContainer := fields["container"]
		if !hasHost || !hasContainer {
			return fmt.Errorf("%s needs host and container: %s", label, portsShape)
		}
		h, ok := specInt(host, 1, 65535)
		if !ok {
			return fmt.Errorf("%s.host must be an integer from 1 to 65535", label)
		}
		if _, ok := specInt(container, 1, 65535); !ok {
			return fmt.Errorf("%s.container must be an integer from 1 to 65535", label)
		}
		if public, ok := fields["public"]; ok && !specBool(public) {
			return fmt.Errorf("%s.public must be true or false (false binds 127.0.0.1, true binds 0.0.0.0)", label)
		}
		if _, dup := hosts[h]; dup {
			return fmt.Errorf("%s.host repeats an earlier host port; host ports must be unique", label)
		}
		hosts[h] = struct{}{}
	}
	return nil
}

// validateVolumes is ImageSpec rule 8. It returns the targets for the files
// overlap check.
func validateVolumes(raw json.RawMessage) ([]string, error) {
	items, _, err := decodeSpecList(raw, "volumes", volumesShape, volumesMax)
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for i, item := range items {
		label := fmt.Sprintf("volumes[%d]", i)
		fields, err := decodeSpecItem(item, label, volumesShape, "name", "target", "read_only")
		if err != nil {
			return nil, err
		}
		name, _ := specString(fields["name"])
		if !volumeNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%s.name must be lowercase letters, digits and -, at most 48 characters: %s", label, volumesShape)
		}
		target, _ := specString(fields["target"])
		if !validVolumeTarget(target) {
			return nil, fmt.Errorf("%s.target must be an absolute path of at most 255 bytes with no . or .. segment", label)
		}
		if ro, ok := fields["read_only"]; ok && !specBool(ro) {
			return nil, fmt.Errorf("%s.read_only must be true or false", label)
		}
		if _, dup := seen[target]; dup {
			return nil, fmt.Errorf("%s.target repeats an earlier volume target; targets must be unique", label)
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

// validateFiles is ImageSpec rule 13, except that the file must exist in the
// project, which only the API can check ("unknown file").
func validateFiles(raw json.RawMessage, volumeTargets []string) error {
	items, _, err := decodeSpecList(raw, "files", filesShape, filesMax)
	if err != nil {
		return err
	}
	names := map[string]struct{}{}
	targets := map[string]struct{}{}
	for i, item := range items {
		label := fmt.Sprintf("files[%d]", i)
		fields, err := decodeSpecItem(item, label, filesShape, "file", "target")
		if err != nil {
			return err
		}
		name, _ := specString(fields["file"])
		if !specFileNamePattern.MatchString(name) {
			return fmt.Errorf("%s.file must be a project file name (lowercase letters, digits, ., _ and -, at most 64 characters): %s", label, filesShape)
		}
		target, _ := specString(fields["target"])
		if !validFileMountTarget(target) {
			return fmt.Errorf("%s.target must be an absolute path of at most 255 bytes, with no empty, . or .. segment, no trailing /, and not under /proc, /sys or /dev", label)
		}
		if _, dup := names[name]; dup {
			return fmt.Errorf("%s.file repeats an earlier file; each file is mounted once", label)
		}
		if _, dup := targets[target]; dup {
			return fmt.Errorf("%s.target repeats an earlier file target; targets must be unique", label)
		}
		for j, vol := range volumeTargets {
			if mountTargetsOverlap(target, vol) {
				return fmt.Errorf("%s.target overlaps volumes[%d].target; a file may not sit at, under or above a volume", label, j)
			}
		}
		names[name] = struct{}{}
		targets[target] = struct{}{}
	}
	return nil
}

func validVolumeTarget(target string) bool {
	if target == "" || len(target) > mountTargetMax || !strings.HasPrefix(target, "/") {
		return false
	}
	if strings.ContainsRune(target, 0) || strings.ContainsAny(target, "\n\r") {
		return false
	}
	for _, seg := range strings.Split(target, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func validFileMountTarget(target string) bool {
	if target == "" || target == "/" || len(target) > mountTargetMax {
		return false
	}
	if !strings.HasPrefix(target, "/") || strings.HasSuffix(target, "/") {
		return false
	}
	if strings.ContainsRune(target, 0) || strings.ContainsAny(target, "\n\r") {
		return false
	}
	for _, seg := range strings.Split(target, "/")[1:] {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for _, root := range []string{"/proc", "/sys", "/dev"} {
		if target == root || strings.HasPrefix(target, root+"/") {
			return false
		}
	}
	return true
}

// mountTargetsOverlap is the API's file and volume overlap rule.
func mountTargetsOverlap(fileTarget, volumeTarget string) bool {
	file := path.Clean(fileTarget)
	volume := path.Clean(volumeTarget)
	if volume == "/" || file == volume {
		return true
	}
	return strings.HasPrefix(file, volume+"/") || strings.HasPrefix(volume, file+"/")
}
