package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

func intRef(v int) *int { return &v }

func TestGenerateShapes(t *testing.T) {
	cases := []struct {
		gen  Generate
		re   *regexp.Regexp
		size int
	}{
		{Generate{Kind: GenPassword}, regexp.MustCompile(`^[A-Za-z0-9]+$`), 32},
		{Generate{Kind: GenPassword, Length: intRef(16)}, regexp.MustCompile(`^[A-Za-z0-9]+$`), 16},
		{Generate{Kind: GenPassword, Length: intRef(64)}, regexp.MustCompile(`^[A-Za-z0-9]+$`), 64},
		{Generate{Kind: GenHex}, regexp.MustCompile(`^[0-9a-f]+$`), 64},
		{Generate{Kind: GenBase64URL}, regexp.MustCompile(`^[A-Za-z0-9_-]+$`), 43},
	}
	for _, tc := range cases {
		seen := map[string]bool{}
		for i := 0; i < 1000; i++ {
			v, err := tc.gen.NewValue()
			if err != nil {
				t.Fatal(err)
			}
			if len(v) != tc.size || !tc.re.MatchString(v) {
				t.Fatalf("%s value of length %d does not match", tc.gen.Kind, len(v))
			}
			if strings.ContainsAny(v, "$=\n\r\x00") {
				t.Fatalf("%s value has a reserved character", tc.gen.Kind)
			}
			if seen[v] {
				t.Fatalf("%s repeated a value in 1000 draws", tc.gen.Kind)
			}
			seen[v] = true
		}
	}
	v, err := Generate{Kind: GenHex}.NewValue()
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(v); err != nil || len(raw) != 32 {
		t.Fatalf("hex decodes to %d bytes %v", len(raw), err)
	}
	v, err = Generate{Kind: GenBase64URL}.NewValue()
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(v); err != nil || len(raw) != 32 {
		t.Fatalf("base64url decodes to %d bytes %v", len(raw), err)
	}
}

func TestGenerateValidate(t *testing.T) {
	for _, tc := range []struct {
		gen Generate
		msg string
	}{
		{Generate{Kind: "uuid"}, `generate.kind must be "password", "hex" or "base64url"`},
		{Generate{}, `generate.kind must be "password", "hex" or "base64url"`},
		{Generate{Kind: GenPassword, Length: intRef(15)}, "generate.length must be 16 to 64"},
		{Generate{Kind: GenPassword, Length: intRef(65)}, "generate.length must be 16 to 64"},
		{Generate{Kind: GenHex, Length: intRef(32)}, "generate.length is only for kind password"},
		{Generate{Kind: GenBase64URL, Length: intRef(32)}, "generate.length is only for kind password"},
	} {
		err := tc.gen.Validate()
		fe, ok := err.(*FieldError)
		if !ok || fe.Code != CodeBadRequest || fe.Msg != tc.msg {
			t.Fatalf("%#v: %v", tc.gen, err)
		}
		if _, err := tc.gen.NewValue(); err == nil {
			t.Fatalf("%#v made a value", tc.gen)
		}
	}
	if (Generate{Kind: GenPassword}).Chars() != 32 || (Generate{Kind: GenPassword, Length: intRef(20)}).Chars() != 20 || (Generate{Kind: GenHex}).Chars() != 0 {
		t.Fatal("chars")
	}
}

// TestPasswordRejectsBiasedBytes feeds bytes at and above the cutoff: they
// must be skipped, never folded into the alphabet with a modulo.
func TestPasswordRejectsBiasedBytes(t *testing.T) {
	if passwordCutoff != 248 {
		t.Fatalf("cutoff %d", passwordCutoff)
	}
	src := bytes.NewReader(append(bytes.Repeat([]byte{248, 255}, 8), bytes.Repeat([]byte{0, 61, 62, 247}, 8)...))
	v, err := randomPassword(src, 16)
	if err != nil {
		t.Fatal(err)
	}
	if v != strings.Repeat("A9A9", 4) {
		t.Fatalf("got %q", v)
	}
	if _, err := randomPassword(bytes.NewReader([]byte{1, 2}), 16); err == nil {
		t.Fatal("short source made a value")
	}
}

// TestPasswordDistribution is a sanity check on character frequency over
// 62 000 characters: each should be near 1000. The bound is loose (about
// eight standard deviations) so it does not flake.
func TestPasswordDistribution(t *testing.T) {
	counts := map[byte]int{}
	for i := 0; i < 1000; i++ {
		v, err := Generate{Kind: GenPassword, Length: intRef(62)}.NewValue()
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < len(v); j++ {
			counts[v[j]]++
		}
	}
	if len(counts) != len(passwordAlphabet) {
		t.Fatalf("%d distinct characters", len(counts))
	}
	for c, n := range counts {
		if n < 750 || n > 1250 {
			t.Fatalf("character %q seen %d times", c, n)
		}
	}
}
