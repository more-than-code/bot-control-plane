package changelog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmbeddedChangelogParses(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatalf("CHANGELOG.md: %v", err)
	}
	latest, ok := Latest()
	if !ok || latest.Version != Version() || len(latest.Items) == 0 {
		t.Fatalf("latest %#v ok=%v version %q", latest, ok, Version())
	}
	for _, e := range Entries(10) {
		if n := len(e.Items); n < 3 || n > 8 {
			t.Errorf("v%s has %d bullets; write 3–8", e.Version, n)
		}
		for _, item := range e.Items {
			if n := utf8.RuneCountInString(item); n > 300 {
				t.Errorf("v%s bullet is %d runes; keep it to 300: %q", e.Version, n, item)
			}
		}
	}
}

// Spec-update-awareness §5 item 1: a forgotten version bump fails the gates.
func TestNewestVersionMatchesConsolePackage(t *testing.T) {
	data, err := os.ReadFile("console/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Version != Version() {
		t.Fatalf("console/package.json version %q, CHANGELOG.md newest %q", pkg.Version, Version())
	}
}

func TestParseGood(t *testing.T) {
	got, err := Parse("# Changelog\n\nSome prose.\n\n## v1.10.0 — 2026-11-20\n- New `tool`.\n- Fix.\n\n## v1.9.2 — 2026-11-01\n- Older.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != "1.10.0" || got[0].Date != "2026-11-20" || len(got[0].Items) != 2 || got[0].Items[0] != "New `tool`." {
		t.Fatalf("got %#v", got)
	}
	if got[1].Version != "1.9.2" || got[1].Items[0] != "Older." {
		t.Fatalf("got %#v", got[1])
	}
}

func TestParseMalformed(t *testing.T) {
	for name, text := range map[string]string{
		"empty":            "",
		"title only":       "# Changelog\n",
		"no v":             "## 0.2.0 — 2026-11-01\n- a\n",
		"hyphen not dash":  "## v0.2.0 - 2026-11-01\n- a\n",
		"two-part version": "## v0.2 — 2026-11-01\n- a\n",
		"bad date":         "## v0.2.0 — 2026-13-01\n- a\n",
		"no bullets":       "## v0.2.1 — 2026-11-02\n## v0.2.0 — 2026-11-01\n- a\n",
		"last no bullets":  "## v0.2.1 — 2026-11-02\n- a\n## v0.2.0 — 2026-11-01\n",
		"bullet first":     "- a\n## v0.2.0 — 2026-11-01\n- a\n",
		"prose in entry":   "## v0.2.0 — 2026-11-01\n- a\nprose\n",
		"star bullet":      "## v0.2.0 — 2026-11-01\n* a\n",
		"empty bullet":     "## v0.2.0 — 2026-11-01\n- \n",
		"h3 in entry":      "## v0.2.0 — 2026-11-01\n- a\n### more\n",
		"older first":      "## v0.2.0 — 2026-11-01\n- a\n## v0.2.1 — 2026-11-02\n- b\n",
		"duplicate":        "## v0.2.0 — 2026-11-01\n- a\n## v0.2.0 — 2026-11-01\n- b\n",
		"numeric order":    "## v0.2.9 — 2026-11-01\n- a\n## v0.2.10 — 2026-11-02\n- b\n",
		"leading zero":     "## v0.02.0 — 2026-11-01\n- a\n",
		"ten digits":       "## v1234567890.0.0 — 2026-11-01\n- a\n",
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestEntriesCountAndCopy(t *testing.T) {
	all := Entries(100)
	if len(all) == 0 || len(Entries(1)) != 1 || len(Entries(0)) != 0 || len(Entries(-1)) != 0 {
		t.Fatalf("entries %d", len(all))
	}
	e := Entries(1)[0]
	e.Items[0] = "changed"
	if strings.HasPrefix(Entries(1)[0].Items[0], "changed") {
		t.Fatal("Entries shares its slice")
	}
}
