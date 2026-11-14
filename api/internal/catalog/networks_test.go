package catalog

import (
	"errors"
	"strings"
	"testing"
)

// goldenSpec is a full v0.2.31 spec without networks. Its stored form and
// hash were taken from v0.2.31 (618ed83) and must not change (api-v1 v1.25).
const goldenSpec = `{"kind":"service","image":"ghcr.io/acme/web:1.2","command":["serve","--port","8080"],"ports":[{"host":8080,"container":8080,"public":false}],"env":{"A":"1"},"env_sets":["app"],"volumes":[{"name":"data","target":"/data"}],"healthcheck":{"test":["true"]},"logging":{"max_size_mb":10,"max_files":3},"memory_mb":256,"user":"1000"}`

const goldenStored = `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"files":[],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`

const goldenHash = "b4c4b48ad3d0dd0b3e47999c73f6d1d5684d031afb044ff9f4373761050b4b94"

func goldenDigests() map[string]string {
	return map[string]string{"app": EnvKeyDigest([]string{"X"})}
}

func TestNetworksAbsentKeepsGolden(t *testing.T) {
	sets := SetKeys{"app": {"X"}}
	for _, extra := range []string{"", `,"networks":null`, `,"networks":[]`} {
		raw := strings.TrimSuffix(goldenSpec, "}") + extra + "}"
		img, err := BuildImage("web", []byte(raw), sets, nil)
		if err != nil {
			t.Fatalf("%q: %v", extra, err)
		}
		if img.Networks != nil {
			t.Fatalf("%q: networks %v", extra, img.Networks)
		}
		if got := SpecJSON(img); got != goldenStored {
			t.Fatalf("%q: stored %s", extra, got)
		}
		if got := ImageHash(img, goldenDigests()); got != goldenHash {
			t.Fatalf("%q: hash %s", extra, got)
		}
		back, err := ParseStored([]byte(SpecJSON(img)))
		if err != nil || back.Networks != nil {
			t.Fatalf("%q: parse stored %v %v", extra, back.Networks, err)
		}
	}
}

func TestNetworksRules(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"upper", `["DD"]`},
		{"leading dash", `["-dd"]`},
		{"underscore", `["d_d"]`},
		{"too long", `["` + strings.Repeat("a", 33) + `"]`},
		{"empty name", `[""]`},
		{"duplicate", `["dd","dd"]`},
		{"five", `["a","b","c","d","e"]`},
		{"not array", `"dd"`},
		{"number", `[1]`},
		{"object", `{"dd":true}`},
	}
	for _, tc := range cases {
		raw := `{"kind":"service","image":"nginx","networks":` + tc.val + `}`
		_, err := BuildImage("web", []byte(raw), nil, nil)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Msg != "invalid networks" || fe.Code != CodeBadRequest {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	for _, kind := range []string{"service", "job"} {
		img, err := BuildImage("web", []byte(`{"kind":"`+kind+`","image":"nginx","networks":["dd","a-1","`+strings.Repeat("z", 32)+`","x"]}`), nil, nil)
		if err != nil || len(img.Networks) != 4 || img.Networks[0] != "dd" {
			t.Fatalf("%s: %v %v", kind, img.Networks, err)
		}
	}
}

func TestNetworksStoredAndHashed(t *testing.T) {
	sets := SetKeys{"app": {"X"}}
	raw := strings.TrimSuffix(goldenSpec, "}") + `,"networks":["dd","edge"]}`
	img, err := BuildImage("web", []byte(raw), sets, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := SpecJSON(img)
	want := `"memory_mb":256,"networks":["dd","edge"],"ports":`
	if !strings.Contains(stored, want) || !strings.Contains(CanonicalImage(img, goldenDigests()), want) {
		t.Fatalf("stored %s", stored)
	}
	if ImageHash(img, goldenDigests()) == goldenHash {
		t.Fatal("networks did not enter the hash")
	}
	// GET then PUT: the stored spec is accepted as a write body and gives
	// the same stored spec and hash.
	back, err := ParseStored([]byte(stored))
	if err != nil || len(back.Networks) != 2 || back.Networks[1] != "edge" {
		t.Fatalf("parse stored %v %v", back.Networks, err)
	}
	again, err := BuildImage("web", []byte(stored), sets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if SpecJSON(again) != stored || ImageHash(again, goldenDigests()) != ImageHash(img, goldenDigests()) {
		t.Fatalf("round trip %s", SpecJSON(again))
	}
}

// v0231Goldens are exact stored specs and hashes computed on v0.2.31
// (618ed83, exported with git archive) for a minimal job with timeout_sec and
// a service with health_policy and files. The current code must reproduce
// them (api-v1 v1.25 item 4: an entry without networks is unchanged).
var v0231Goldens = []struct {
	name, raw, stored, hash string
	files                   map[string]struct{}
}{
	{
		name:   "job with timeout_sec",
		raw:    `{"kind":"job","image":"busybox:1.36","command":["true"],"timeout_sec":120}`,
		stored: `{"command":["true"],"entrypoint":null,"env":{},"env_sets":[],"files":[],"healthy_after_sec":0,"image":"docker.io/library/busybox:1.36","kind":"job","memory_mb":null,"ports":[],"restart":"no","secret_env":{},"timeout_sec":120,"user":"","volumes":[]}`,
		hash:   "664fbdda7dd50fb5df1a22bb1a2b86b6a6e969e26cac1ceba4e68c540e60804f",
	},
	{
		name:   "health_policy and files",
		raw:    `{"kind":"service","image":"nginx:1.27","healthcheck":{"test":["true"]},"health_policy":{"on_deploy_failure":"stop","on_unhealthy":"restart","unhealthy_after_sec":60,"max_actions_per_hour":2},"files":[{"file":"app.conf","target":"/etc/app.conf"}]}`,
		stored: `{"command":null,"entrypoint":null,"env":{},"env_sets":[],"files":[{"file":"app.conf","target":"/etc/app.conf"}],"health_policy":{"max_actions_per_hour":2,"on_deploy_failure":"stop","on_unhealthy":"restart","unhealthy_after_sec":60},"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"docker.io/library/nginx:1.27","kind":"service","memory_mb":null,"ports":[],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"","volumes":[]}`,
		hash:   "582075699ed79a21a28e42d95a54b0800ed4494673d07ca35df2019764948ba8",
		files:  map[string]struct{}{"app.conf": {}},
	},
}

func TestV0231Goldens(t *testing.T) {
	for _, g := range v0231Goldens {
		img, err := BuildImage("web", []byte(g.raw), nil, g.files)
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		if got := SpecJSON(img); got != g.stored {
			t.Fatalf("%s stored %s", g.name, got)
		}
		if got := ImageHash(img, nil); got != g.hash {
			t.Fatalf("%s hash %s", g.name, got)
		}
		back, err := ParseStored([]byte(g.stored))
		if err != nil || back.Networks != nil {
			t.Fatalf("%s parse stored %v %v", g.name, back.Networks, err)
		}
	}
}

// TestNetworksGoldenNew pins the stored spec and hash of an entry with
// networks ["dd","edge"]. These values were taken from the new (v1.25) code,
// not from v0.2.31; they pin key order and the hash from now on.
func TestNetworksGoldenNew(t *testing.T) {
	const stored = `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"files":[],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"networks":["dd","edge"],"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`
	const hash = "16cafa54b647597bd1a3b7e45a927077ac219806c0d62cedf73ab6aeaeeaa687"
	raw := strings.TrimSuffix(goldenSpec, "}") + `,"networks":["dd","edge"]}`
	img, err := BuildImage("web", []byte(raw), SetKeys{"app": {"X"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := SpecJSON(img); got != stored {
		t.Fatalf("stored %s", got)
	}
	if got := ImageHash(img, goldenDigests()); got != hash {
		t.Fatalf("hash %s", got)
	}
}

// ParseStored re-checks stored network names, so a row written outside the
// API never carries an unchecked name into a refusal, plan, audit or task.
func TestParseStoredRejectsBadNetworks(t *testing.T) {
	base := strings.TrimSuffix(v0231Goldens[0].stored, "}")
	for _, nets := range []string{
		`["DD"]`,
		`["dd; rm -rf /"]`,
		`["dd\nforged"]`,
		`["` + strings.Repeat("a", 33) + `"]`,
		`["dd","dd"]`,
		`["a","b","c","d","e"]`,
		`[""]`,
	} {
		if _, err := ParseStored([]byte(base + `,"networks":` + nets + `}`)); err == nil {
			t.Fatalf("%s accepted", nets)
		}
	}
	for _, nets := range []string{`null`, `[]`, `["dd"]`} {
		img, err := ParseStored([]byte(base + `,"networks":` + nets + `}`))
		if err != nil {
			t.Fatalf("%s: %v", nets, err)
		}
		if nets != `["dd"]` && img.Networks != nil {
			t.Fatalf("%s networks %v", nets, img.Networks)
		}
	}
}
