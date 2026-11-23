// Package hostpolicy is the server side of a host's console-managed policy
// (api-v1 v1.51, agent-protocol v1.22): the runner.toml image pattern and
// project slug rules, copied from bot-agent so the API refuses what the
// host would refuse, the pattern match, and the change summary for audit.
package hostpolicy

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// MaxImages and MaxProjects are the host's limits on one apply.
	MaxImages   = 64
	MaxProjects = 32
	maxRefBytes = 255
)

var (
	nameCompRE   = regexp.MustCompile(`^[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?$`)
	domainCompRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	projectRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	hashRE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidHash is a lowercase hex SHA-256.
func ValidHash(s string) bool { return hashRE.MatchString(s) }

// ValidProject is the runner.toml project slug rule.
func ValidProject(s string) bool { return projectRE.MatchString(s) }

// ValidPattern is the runner.toml image pattern rule: a literal registry
// host, then path components where "*" stands for one whole component.
func ValidPattern(p string) bool {
	_, _, ok := splitPattern(p)
	return ok
}

// Match reports whether the image host and path (as catalog.ImageRef gives
// them) equal one of the patterns.
func Match(host, path string, patterns []string) bool {
	comps := strings.Split(path, "/")
	for _, p := range patterns {
		ph, pc, ok := splitPattern(p)
		if !ok || ph != host || len(pc) != len(comps) {
			continue
		}
		hit := true
		for i, c := range pc {
			if c != "*" && c != comps[i] {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

func splitPattern(p string) (string, []string, bool) {
	if p == "" || len(p) > maxRefBytes || strings.ContainsAny(p, " \t\r\n\x00?#\\%@") {
		return "", nil, false
	}
	host, rest, ok := strings.Cut(p, "/")
	if !ok || host == "" || rest == "" || strings.Contains(host, "*") || !validHost(host) {
		return "", nil, false
	}
	comps := strings.Split(rest, "/")
	for _, c := range comps {
		if c == "*" {
			continue
		}
		if strings.Contains(c, "*") || !nameCompRE.MatchString(c) {
			return "", nil, false
		}
	}
	return host, comps, true
}

func validHost(host string) bool {
	name := host
	if h, port, ok := strings.Cut(host, ":"); ok {
		n, err := strconv.Atoi(port)
		if strings.Contains(port, ":") || err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return false
		}
		name = h
	}
	if name == "" {
		return false
	}
	for _, c := range strings.Split(name, ".") {
		if !domainCompRE.MatchString(c) {
			return false
		}
	}
	return true
}

// Policy is the console-editable part of a host's runner.toml.
type Policy struct {
	Edge     bool     `json:"edge"`
	Images   []string `json:"images"`
	Projects []string `json:"projects"`
}

// Check returns "" when p is one the host accepts, else a plain reason.
func Check(p Policy) string {
	if p.Images == nil || p.Projects == nil {
		return "images and projects are required"
	}
	if len(p.Images) > MaxImages {
		return "at most " + strconv.Itoa(MaxImages) + " image patterns"
	}
	if len(p.Projects) > MaxProjects {
		return "at most " + strconv.Itoa(MaxProjects) + " projects"
	}
	seen := map[string]bool{}
	for _, x := range p.Images {
		if !ValidPattern(x) {
			return "image pattern " + strconv.Quote(x) + " is not valid; use a registry host and a path, with * for one whole path part"
		}
		if seen[x] {
			return "image pattern " + x + " is listed twice"
		}
		seen[x] = true
	}
	seen = map[string]bool{}
	for _, x := range p.Projects {
		if !ValidProject(x) {
			return "project " + strconv.Quote(x) + " is not a project slug"
		}
		if seen[x] {
			return "project " + x + " is listed twice"
		}
		seen[x] = true
	}
	return ""
}

// Change is what an apply changes, for audit and the console.
type Change struct {
	EdgeFrom        bool     `json:"edge_from"`
	EdgeTo          bool     `json:"edge_to"`
	ImagesAdded     []string `json:"images_added"`
	ImagesRemoved   []string `json:"images_removed"`
	ProjectsAdded   []string `json:"projects_added"`
	ProjectsRemoved []string `json:"projects_removed"`
}

// Diff is the change from a to b. Lists are never nil.
func Diff(a, b Policy) Change {
	return Change{
		EdgeFrom: a.Edge, EdgeTo: b.Edge,
		ImagesAdded: minus(b.Images, a.Images), ImagesRemoved: minus(a.Images, b.Images),
		ProjectsAdded: minus(b.Projects, a.Projects), ProjectsRemoved: minus(a.Projects, b.Projects),
	}
}

// Empty reports a change that changes nothing.
func (c Change) Empty() bool {
	return c.EdgeFrom == c.EdgeTo && len(c.ImagesAdded)+len(c.ImagesRemoved)+len(c.ProjectsAdded)+len(c.ProjectsRemoved) == 0
}

func minus(xs, ys []string) []string {
	out := []string{}
	for _, x := range xs {
		if !slices.Contains(ys, x) {
			out = append(out, x)
		}
	}
	return out
}
