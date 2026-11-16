package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	changelog "github.com/more-than-code/bot-control-plane"
)

// spec-update-awareness §2.3: whats_new is read-only and says it calls nothing.
func TestWhatsNewDescription(t *testing.T) {
	d, ok := listToolDescriptions(t)["whats_new"]
	if !ok {
		t.Fatal("whats_new not registered")
	}
	for _, phrase := range []string{"newest first", "count is 1–10 (default 3)", "Does not contact any host or call the API"} {
		if !strings.Contains(d, phrase) {
			t.Errorf("description %q missing %q", d, phrase)
		}
	}
}

func TestInstructionsNameVersionAndThreeItems(t *testing.T) {
	latest, ok := changelog.Latest()
	if !ok {
		t.Fatal("changelog did not parse")
	}
	got := Instructions()
	if n := utf8.RuneCountInString(got); n > 1500 {
		t.Fatalf("instructions are %d runes; keep them to 1500", n)
	}
	if !strings.HasPrefix(got, "Deploybot ") {
		t.Fatalf("instructions start %q", got)
	}
	items := latest.Items
	if len(items) > 3 {
		items = items[:3]
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = strings.TrimRight(item, ".")
	}
	want := " Server v" + latest.Version + " (" + latest.Date + "). New in this release: " + strings.Join(parts, "; ") + ". Call whats_new for the full list."
	if !strings.HasSuffix(got, want) {
		t.Fatalf("instructions\n got %q\nwant suffix %q", got, want)
	}
	if len(latest.Items) > 3 && strings.Contains(got, strings.TrimRight(latest.Items[3], ".")) {
		t.Fatal("instructions carry more than 3 items")
	}
}

func TestSummarizeWhatsNew(t *testing.T) {
	got := summarizeWhatsNew("0.2.37", []changelog.Entry{
		{Version: "0.2.37", Date: "2026-11-17", Items: []string{"A.", "B `x`."}},
		{Version: "0.2.36", Date: "2026-11-16", Items: []string{"C."}},
	})
	want := "Deploybot server v0.2.37\n\nv0.2.37 (2026-11-17)\n- A.\n- B `x`.\n\nv0.2.36 (2026-11-16)\n- C."
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	long := summarizeWhatsNew("1.0.0", []changelog.Entry{{Version: "1.0.0", Date: "2026-11-17\n", Items: []string{strings.Repeat("x", 400)}}})
	if strings.Contains(long, "2026-11-17\n") || strings.Contains(long, strings.Repeat("x", 300)) || !strings.Contains(long, strings.Repeat("x", 299)+"…") {
		t.Fatalf("summary not clamped: %q", long)
	}
}

// api-v1 v1.34 item 3: list_hosts names the newer agent for a host that has one.
func TestSummarizeHostsAgentUpdate(t *testing.T) {
	payload := []byte(`{"items":[
		{"name":"pi1","status":"online","access_level":"deploy","agent_version":"0.2.9","latest_agent_version":"0.2.10","update_available":true},
		{"name":"pi2","status":"online","access_level":"metrics","agent_version":"0.2.10","latest_agent_version":"0.2.10","update_available":false},
		{"name":"pi3","status":"online","agent_version":"0.2.9","latest_agent_version":null,"update_available":false},
		{"name":"pi4","status":"online","agent_version":"0.2.9"}
	]}`)
	want := "4 hosts: pi1 online (deploy) (agent update available: 0.2.10), pi2 online (metrics), pi3 online, pi4 online"
	if got := summarizeHosts(payload, false); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	org := []byte(`{"items":[{"name":"pi1","status":"online","access_level":null,"latest_agent_version":"1.0.0","update_available":true}]}`)
	if got, want := summarizeHosts(org, true), "1 hosts: pi1 online (agent update available: 1.0.0)"; got != want {
		t.Fatalf("org got %q", got)
	}
}
