package hostpolicy

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidPattern(t *testing.T) {
	for p, want := range map[string]bool{
		"ghcr.io/kea-studio/marketing-os-prototype": true,
		"ghcr.io/kea-studio/*":                      true,
		"localhost:5000/app":                        true,
		"ghcr.io":                                   false,
		"*/app":                                     false,
		"ghcr.io/kea-*":                             false,
		"ghcr.io/Kea":                               false,
		"ghcr.io/a b":                               false,
		"":                                          false,
	} {
		if ValidPattern(p) != want {
			t.Errorf("%q: want %v", p, want)
		}
	}
}

func TestMatch(t *testing.T) {
	pats := []string{"ghcr.io/kea-studio/*", "docker.io/library/caddy"}
	if !Match("ghcr.io", "kea-studio/site", pats) || !Match("docker.io", "library/caddy", pats) {
		t.Fatal("missed")
	}
	if Match("ghcr.io", "kea-studio/a/b", pats) || Match("ghcr.io", "other/site", pats) || Match("quay.io", "kea-studio/site", pats) {
		t.Fatal("matched too much")
	}
}

func TestCheckAndDiff(t *testing.T) {
	if r := Check(Policy{Images: []string{"ghcr.io/a/b"}, Projects: []string{"acme"}}); r != "" {
		t.Fatal(r)
	}
	for _, bad := range []Policy{
		{Projects: []string{}},
		{Images: []string{"x"}, Projects: []string{}},
		{Images: []string{"ghcr.io/a/b", "ghcr.io/a/b"}, Projects: []string{}},
		{Images: []string{}, Projects: []string{"Acme"}},
		{Images: []string{}, Projects: strings.Split(strings.Repeat("a,", 33), ",")},
	} {
		if Check(bad) == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
	d := Diff(Policy{Images: []string{"a/x"}, Projects: []string{"p"}}, Policy{Edge: true, Images: []string{"a/x", "a/y"}, Projects: []string{}})
	want := Change{EdgeFrom: false, EdgeTo: true, ImagesAdded: []string{"a/y"}, ImagesRemoved: []string{}, ProjectsAdded: []string{}, ProjectsRemoved: []string{"p"}}
	if !reflect.DeepEqual(d, want) || d.Empty() {
		t.Fatalf("%+v", d)
	}
	if !Diff(Policy{Images: []string{"a"}, Projects: []string{}}, Policy{Images: []string{"a"}, Projects: []string{}}).Empty() {
		t.Fatal("not empty")
	}
}
