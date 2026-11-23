package catalog

import (
	"errors"
	"strings"
	"testing"
)

// api-v1 v1.50 item 1: ImageSpec network is absent or "host"; anything else,
// or "host" with shared networks, is bad_request.
func TestNetworkHostRules(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  string // "" = accepted
		mode  string
	}{
		{"absent", ``, "", ""},
		{"null", `,"network":null`, "", ""},
		{"host", `,"network":"host"`, "", NetworkHost},
		{"host job", `,"network":"host","kind":"job"`, "", NetworkHost},
		{"host with empty networks", `,"network":"host","networks":[]`, "", NetworkHost},
		{"host with null networks", `,"network":"host","networks":null`, "", NetworkHost},
		{"bridge", `,"network":"bridge"`, "invalid network", ""},
		{"none", `,"network":"none"`, "invalid network", ""},
		{"empty string", `,"network":""`, "invalid network", ""},
		{"upper", `,"network":"HOST"`, "invalid network", ""},
		{"number", `,"network":1`, "invalid network", ""},
		{"array", `,"network":["host"]`, "invalid network", ""},
		{"host with networks", `,"network":"host","networks":["dd"]`, "network host cannot join shared networks", ""},
	}
	for _, tc := range cases {
		kind := `"kind":"service",`
		if strings.Contains(tc.extra, `"kind"`) {
			kind = ""
		}
		raw := `{` + kind + `"image":"nginx"` + tc.extra + `}`
		img, err := BuildImage("web", []byte(raw), nil, nil)
		if tc.want == "" {
			if err != nil || img.Network != tc.mode {
				t.Fatalf("%s: %q %v", tc.name, img.Network, err)
			}
			continue
		}
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Msg != tc.want || fe.Code != CodeBadRequest {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

// An entry without network keeps its stored spec and hash (the golden); with
// "host" the stored spec carries it in key order, the hash changes, and GET
// then PUT round-trips.
func TestNetworkHostStoredAndHashed(t *testing.T) {
	sets := SetKeys{"app": {"X"}}
	for _, extra := range []string{"", `,"network":null`} {
		img, err := BuildImage("web", []byte(strings.TrimSuffix(goldenSpec, "}")+extra+"}"), sets, nil)
		if err != nil {
			t.Fatal(err)
		}
		if SpecJSON(img) != goldenStored || ImageHash(img, goldenDigests()) != goldenHash {
			t.Fatalf("%q changed the golden: %s", extra, SpecJSON(img))
		}
	}
	img, err := BuildImage("web", []byte(strings.TrimSuffix(goldenSpec, "}")+`,"network":"host"}`), sets, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := SpecJSON(img)
	want := `"memory_mb":256,"network":"host","ports":`
	if !strings.Contains(stored, want) || !strings.Contains(CanonicalImage(img, goldenDigests()), want) {
		t.Fatalf("stored %s", stored)
	}
	if ImageHash(img, goldenDigests()) == goldenHash {
		t.Fatal("network did not enter the hash")
	}
	back, err := ParseStored([]byte(stored))
	if err != nil || back.Network != NetworkHost {
		t.Fatalf("parse stored %q %v", back.Network, err)
	}
	again, err := BuildImage("web", []byte(stored), sets, nil)
	if err != nil || SpecJSON(again) != stored {
		t.Fatalf("round trip %v %s", err, SpecJSON(again))
	}
}

// ParseStored re-checks network, so a row written outside the API cannot
// send an unchecked mode to an agent.
func TestParseStoredRejectsBadNetwork(t *testing.T) {
	base := strings.TrimSuffix(v0231Goldens[0].stored, "}")
	for _, extra := range []string{`,"network":"bridge"`, `,"network":"host","networks":["dd"]`, `,"network":1`} {
		if _, err := ParseStored([]byte(base + extra + `}`)); err == nil {
			t.Fatalf("%s accepted", extra)
		}
	}
}
