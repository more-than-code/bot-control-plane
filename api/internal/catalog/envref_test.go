package catalog

import (
	"errors"
	"strings"
	"testing"
)

// TestRefErrorNames is api-v1 v1.29 item 7: the error names the key and the
// unknown name, also for a secret key whose message leaves the name out.
func TestRefErrorNames(t *testing.T) {
	env := map[string]string{"A": "x${B}", "S": "${C}", "Z": "${Y"}
	_, err := ResolveEnvRefs(env, map[string]bool{"S": true})
	var ref *RefError
	if !errors.As(err, &ref) || ref.Key != "A" || ref.Name != "B" {
		t.Fatalf("plain %#v", err)
	}
	delete(env, "A")
	_, err = ResolveEnvRefs(env, map[string]bool{"S": true})
	if !errors.As(err, &ref) || ref.Key != "S" || ref.Name != "C" || strings.Contains(ref.Msg, "C}") {
		t.Fatalf("secret %#v", err)
	}
	delete(env, "S")
	_, err = ResolveEnvRefs(env, nil)
	if !errors.As(err, &ref) || ref.Key != "Z" || ref.Name != "" {
		t.Fatalf("unterminated %#v", err)
	}
}

func TestResolveEnvRefs(t *testing.T) {
	long := strings.Repeat("A", 65)
	base := map[string]string{
		"PW":    "s3cret",
		"HOST":  "db",
		"LOOP":  "${LOOP}",
		"INNER": "${PW}",
		"EMPTY": "",
		"B":     "bee",
		"X":     "ex",
		long:    "never",
	}
	cases := []struct {
		name, in, want string
	}{
		{"plain", "no refs here", "no refs here"},
		{"one", "postgres://u:${PW}@${HOST}:5432/x", "postgres://u:s3cret@db:5432/x"},
		{"escape", "$${PW}", "${PW}"},
		{"escape then ref", "$${PW}${PW}", "${PW}s3cret"},
		{"triple dollar", "$$${X}", "$${X}"},
		{"lone dollar", "$HOME and $ and $$", "$HOME and $ and $$"},
		{"dollar before ref", "$${HOST}$${", "${HOST}${"},
		{"not a key name", "${lower} ${A:-b} ${}", "${lower} ${A:-b} ${}"},
		{"name over 64 chars", "${" + long + "}", "${" + long + "}"},
		// The text after the first ${ up to } is "A${B", not a name: that ${
		// is literal and scanning continues, so ${B} resolves.
		{"invalid then ref", "${A${B}", "${Abee"},
		{"one pass", "${INNER}", "${PW}"},
		{"self", "${LOOP}", "${LOOP}"},
		{"empty value", "[${EMPTY}]", "[]"},
		{"adjacent", "${HOST}${HOST}", "dbdb"},
		{"multibyte neighbours", "é${HOST}ü—${B}☃", "édbü—bee☃"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"VALUE": tc.in}
			for k, v := range base {
				env[k] = v
			}
			got, err := ResolveEnvRefs(env, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Env["VALUE"] != tc.want {
				t.Fatalf("got %q want %q", got.Env["VALUE"], tc.want)
			}
		})
	}
}

func TestResolveEnvRefsErrors(t *testing.T) {
	long := strings.Repeat("a", 4000)
	cases := []struct {
		name   string
		env    map[string]string
		secret map[string]bool
		msg    string
	}{
		{"unknown", map[string]string{"DATABASE_URL": "x${SMTP_HOST}"}, nil, "env DATABASE_URL references ${SMTP_HOST}, which is not set for this entry"},
		{"unknown in secret key", map[string]string{"DATABASE_URL": "x${SMTP_HOST}"}, map[string]bool{"DATABASE_URL": true}, "env DATABASE_URL has a reference that is not set for this entry"},
		{"unknown in plain key next to secret", map[string]string{"A": "${NOPE}", "S": "s"}, map[string]bool{"S": true}, "env A references ${NOPE}, which is not set for this entry"},
		{"unterminated", map[string]string{"A": "x${B"}, nil, "env A has an unterminated ${"},
		{"unterminated lowercase", map[string]string{"A": "${low"}, nil, "env A has an unterminated ${"},
		{"unterminated with space", map[string]string{"A": "${a b"}, nil, "env A has an unterminated ${"},
		{"unterminated after ref", map[string]string{"A": "${B}${", "B": "b"}, nil, "env A has an unterminated ${"},
		{"too long", map[string]string{"A": "${B}${B}", "B": long}, nil, "env A is longer than 4096 bytes after references"},
		{"first in key order", map[string]string{"B": "${NOPE}", "A": "${GONE}"}, nil, "env A references ${GONE}, which is not set for this entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveEnvRefs(tc.env, tc.secret)
			re, ok := err.(*RefError)
			if !ok || re.Msg != tc.msg {
				t.Fatalf("err %v", err)
			}
			if strings.Contains(re.Msg, long) {
				t.Fatal("message holds a value")
			}
		})
	}
	// Exactly 4096 bytes is allowed.
	exact := map[string]string{"A": "${B}" + strings.Repeat("c", 96), "B": long}
	if got, err := ResolveEnvRefs(exact, nil); err != nil || len(got.Env["A"]) != 4096 {
		t.Fatalf("4096 bytes %d %v", len(got.Env["A"]), err)
	}
}

func TestResolveEnvRefsSpread(t *testing.T) {
	env := map[string]string{
		"PW":       "pw-value",
		"URL":      "u:${PW}",
		"HOST":     "h",
		"PLAIN":    "${HOST}",
		"TWO":      "${HOST}:${PW}",
		"CHAIN":    "${PLAIN_PW}",
		"PLAIN_PW": "p:${PW}",
		"ESCAPE":   "$${PW}",
	}
	secret := map[string]bool{"PW": true}
	got, err := ResolveEnvRefs(env, secret)
	if err != nil {
		t.Fatal(err)
	}
	spread := got.Spread(secret)
	// TWO references a plain key and then a secret one: secret.
	if !spread["URL"] || !spread["PW"] || !spread["TWO"] || !spread["PLAIN_PW"] || spread["PLAIN"] || spread["HOST"] || spread["ESCAPE"] {
		t.Fatalf("spread %v", spread)
	}
	// CHAIN goes through a plain key: PLAIN_PW is inserted as stored
	// ("p:${PW}"), not re-expanded, so CHAIN holds no secret value.
	if got.Env["CHAIN"] != "p:${PW}" || spread["CHAIN"] {
		t.Fatalf("chain %q %v", got.Env["CHAIN"], spread["CHAIN"])
	}
	if strings.Join(got.Refs["TWO"], ",") != "HOST,PW" || got.Refs["ESCAPE"] != nil {
		t.Fatalf("refs %v", got.Refs)
	}
	if len(secret) != 1 || env["URL"] != "u:${PW}" {
		t.Fatal("inputs changed")
	}
}
