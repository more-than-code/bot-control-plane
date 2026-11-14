package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeOwnerUser(t *testing.T) {
	keep := func(in string) {
		t.Helper()
		got := NormalizeOwnerUser(&in)
		if got == nil || *got != in {
			t.Fatalf("NormalizeOwnerUser(%q) = %v", in, got)
		}
		in = "changed"
		if *got == "changed" {
			t.Fatal("owner_user aliases the caller")
		}
	}
	drop := func(in *string) {
		t.Helper()
		if got := NormalizeOwnerUser(in); got != nil {
			t.Fatalf("NormalizeOwnerUser(%v) = %q", in, *got)
		}
	}
	drop(nil)
	drop(strPtr(""))
	keep("root")
	keep(" www-data ")
	keep("café")
	keep(strings.Repeat("é", 32))
	drop(strPtr(strings.Repeat("é", 33)))
	keep(strings.Repeat("a", MaxOwnerUserBytes))
	drop(strPtr(strings.Repeat("a", MaxOwnerUserBytes+1)))
	drop(strPtr("a\nb"))
	drop(strPtr("a\tb"))
	drop(strPtr("a\x00b"))
	drop(strPtr("bad\xff"))
}

func strPtr(s string) *string { return &s }

func TestOrgNames(t *testing.T) {
	if got := OrgSlug("More Than Code"); got != "more-than-code" {
		t.Fatalf("slug %q", got)
	}
	if got := OrgSlug("!!!"); got != "org" {
		t.Fatalf("empty slug %q", got)
	}
	if got := OrgSlug(strings.Repeat("a", 90)); len(got) != MaxOrgSlug || !OrgSlugRe.MatchString(got) {
		t.Fatalf("long slug %q", got)
	}
	id := uuid.MustParse("3aec6e15-4951-4886-a9f8-7ca0b6ac3e46")
	if got := PersonalOrgSlug(id); got != "u-3aec6e154951" || !OrgSlugRe.MatchString(got) {
		t.Fatalf("personal slug %q", got)
	}
	if got := PersonalOrgName("Joe", "j@example.com"); got != "Joe's org" {
		t.Fatalf("name %q", got)
	}
	if got := PersonalOrgName(" ", "pat@example.com"); got != "pat's org" {
		t.Fatalf("email name %q", got)
	}
	if got := PersonalOrgName(strings.Repeat("é", 100), ""); !ValidName(got) {
		t.Fatalf("long name %q", got)
	}
}
