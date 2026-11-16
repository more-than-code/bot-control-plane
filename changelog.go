// Package changelog reads CHANGELOG.md, embedded at build time, so the MCP
// server and its whats_new tool name the same releases the console shows
// (spec-update-awareness §2). The file is parsed once at init. A malformed
// file leaves the package empty instead of stopping a server; its test fails.
package changelog

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed CHANGELOG.md
var raw string

// Entry is one release: its version (X.Y.Z, no leading v), its date
// (YYYY-MM-DD) and its bullets, as written.
type Entry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Items   []string `json:"items"`
}

var (
	entries  []Entry
	parseErr error
)

func init() {
	entries, parseErr = Parse(raw)
}

// Err is the embedded file's parse error, nil when it parsed.
func Err() error { return parseErr }

// Version is the newest release's version, or "unknown" when the file did
// not parse.
func Version() string {
	if len(entries) == 0 {
		return "unknown"
	}
	return entries[0].Version
}

// Latest is the newest release. ok is false when the file did not parse.
func Latest() (Entry, bool) {
	if len(entries) == 0 {
		return Entry{}, false
	}
	return copyEntry(entries[0]), true
}

// Entries returns up to n releases, newest first.
func Entries(n int) []Entry {
	if n > len(entries) {
		n = len(entries)
	}
	out := make([]Entry, 0, max(n, 0))
	for i := 0; i < n; i++ {
		out = append(out, copyEntry(entries[i]))
	}
	return out
}

func copyEntry(e Entry) Entry {
	e.Items = append([]string(nil), e.Items...)
	return e
}

var (
	// Version parts have no leading zeros, as in the console parser ($lib/changelog.ts).
	headingLine = regexp.MustCompile(`^## v(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8}) — (\d{4}-\d{2}-\d{2})$`)
	bulletLine  = regexp.MustCompile(`^- (\S.*)$`)
)

// Parse reads the restricted format: an optional "# " title and prose lines,
// then one or more "## vX.Y.Z — YYYY-MM-DD" headings, each followed by one
// or more "- " bullets. Versions must strictly decrease down the file.
func Parse(text string) ([]Entry, error) {
	var out []Entry
	var prev [3]int
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		n := i + 1
		line = strings.TrimRight(line, " \t")
		switch {
		case line == "":
		case strings.HasPrefix(line, "## "):
			m := headingLine.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("line %d: heading must be \"## vX.Y.Z — YYYY-MM-DD\"", n)
			}
			var v [3]int
			for k := 0; k < 3; k++ {
				x, err := strconv.Atoi(m[k+1])
				if err != nil {
					return nil, fmt.Errorf("line %d: bad version", n)
				}
				v[k] = x
			}
			if _, err := time.Parse("2006-01-02", m[4]); err != nil {
				return nil, fmt.Errorf("line %d: bad date", n)
			}
			if len(out) > 0 {
				if len(out[len(out)-1].Items) == 0 {
					return nil, fmt.Errorf("line %d: v%s has no bullets", n, out[len(out)-1].Version)
				}
				if compare(v, prev) >= 0 {
					return nil, fmt.Errorf("line %d: versions must be newest first", n)
				}
			}
			prev = v
			out = append(out, Entry{Version: fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]), Date: m[4]})
		case strings.HasPrefix(line, "-"):
			m := bulletLine.FindStringSubmatch(line)
			if m == nil || len(out) == 0 {
				return nil, fmt.Errorf("line %d: a bullet is \"- text\" under a release heading", n)
			}
			last := &out[len(out)-1]
			last.Items = append(last.Items, m[1])
		case strings.HasPrefix(line, "# ") && len(out) == 0:
			// The file title.
		case len(out) == 0 && !strings.HasPrefix(line, "#"):
			// Prose before the first release.
		default:
			return nil, fmt.Errorf("line %d: only bullets may follow a release heading", n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no release headings")
	}
	if len(out[len(out)-1].Items) == 0 {
		return nil, fmt.Errorf("v%s has no bullets", out[len(out)-1].Version)
	}
	return out, nil
}

func compare(a, b [3]int) int {
	for k := 0; k < 3; k++ {
		if a[k] != b[k] {
			if a[k] < b[k] {
				return -1
			}
			return 1
		}
	}
	return 0
}
