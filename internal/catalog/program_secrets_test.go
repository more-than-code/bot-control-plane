package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func programIn(env map[string]string, sets []string, secret map[string]string, known SetKeys) ProgramInput {
	return ProgramInput{
		Command: "gh api user", Dir: "/srv/app", RunAs: "joe", Env: env,
		EnvSets: sets, SecretEnv: secret, Sets: known,
	}
}

// api-v1 v1.22 item 1: env_sets and secret_env on a program follow the image
// rules; inline env keeps the secret-looking-key refusal.
func TestProgramEnvSetRules(t *testing.T) {
	known := SetKeys{"gh": {"GH_TOKEN", "GH_HOST"}, "db": {"DB_PASSWORD"}}
	got, err := BuildProgram(programIn(nil, []string{"gh"}, map[string]string{"PGPASSWORD": "db.DB_PASSWORD"}, known))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EnvSets) != 1 || got.SecretEnv["PGPASSWORD"] != "db.DB_PASSWORD" {
		t.Fatalf("refs %#v %#v", got.EnvSets, got.SecretEnv)
	}
	empty, err := BuildProgram(programIn(nil, nil, nil, nil))
	if err != nil || empty.EnvSets == nil || empty.SecretEnv == nil {
		t.Fatalf("empty refs must be non-nil: %#v %#v %v", empty.EnvSets, empty.SecretEnv, err)
	}
	nine := make([]string, 9)
	nineKnown := SetKeys{}
	for i := range nine {
		nine[i] = fmt.Sprintf("s%d", i)
		nineKnown[nine[i]] = nil
	}
	tooManySecret := map[string]string{}
	manyKnown := SetKeys{"big": nil}
	for i := 0; i < 65; i++ {
		k := fmt.Sprintf("K%02d", i)
		tooManySecret[k] = "big." + k
		manyKnown["big"] = append(manyKnown["big"], k)
	}
	bigSet := SetKeys{"big": manyKnown["big"]}
	cases := []struct {
		name string
		in   ProgramInput
		msg  string
	}{
		{"inline secret key", programIn(map[string]string{"GH_TOKEN": "x"}, nil, nil, known), "use secret_env"},
		{"unknown set", programIn(nil, []string{"nope"}, nil, known), "unknown env set"},
		{"duplicate set", programIn(nil, []string{"gh", "gh"}, nil, known), "invalid env_sets"},
		{"bad set name", programIn(nil, []string{"Gh"}, nil, known), "invalid env_sets"},
		{"nine sets", programIn(nil, nine, nil, nineKnown), "invalid env_sets"},
		{"unknown key", programIn(nil, nil, map[string]string{"X": "gh.NOPE"}, known), "unknown env key"},
		{"bad ref", programIn(nil, nil, map[string]string{"X": "gh"}, known), "invalid secret_env"},
		{"reserved", programIn(nil, nil, map[string]string{"PATH": "gh.GH_HOST"}, known), "PATH is reserved"},
		{"changes what runs", programIn(nil, nil, map[string]string{"NODE_OPTIONS": "gh.GH_HOST"}, known), "this variable changes what the program runs"},
		{"65 secret_env", programIn(nil, nil, tooManySecret, manyKnown), "invalid secret_env"},
		{"merged over 64", programIn(map[string]string{"EXTRA": "1"}, []string{"big"}, nil, SetKeys{"big": bigSet["big"][:64]}), "too many env keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildProgram(tc.in)
			fe, ok := err.(*FieldError)
			if !ok || fe.Code != CodeBadRequest || fe.Msg != tc.msg {
				t.Fatalf("got %v want %q", err, tc.msg)
			}
		})
	}
	// Exactly 64 merged keys is accepted; a key in both a set and inline env counts once.
	if _, err := BuildProgram(programIn(map[string]string{"K00": "1"}, []string{"big"}, nil, SetKeys{"big": bigSet["big"][:64]})); err != nil {
		t.Fatalf("64 keys: %v", err)
	}
}

// api-v1 v1.22 item 2: env_keys enters the program hash only when a set is
// referenced; values never do.
func TestProgramHashEnvKeys(t *testing.T) {
	base := Program{
		Dir: "/srv/app", Exec: "gh", Args: []string{"api", "user"}, RunAs: "joe",
		Env: map[string]string{"A": "1"}, EnvSets: []string{}, SecretEnv: map[string]string{}, Restart: "on-failure",
	}
	if ProgramHash(base, map[string]string{"gh": "x"}) != SpecHash(base) {
		t.Fatal("an entry without sets must keep its v1.6 hash")
	}
	withSets := base
	withSets.EnvSets = []string{"gh"}
	withSets.SecretEnv = map[string]string{"PGPASSWORD": "db.DB_PASSWORD"}
	digests := map[string]string{"gh": EnvKeyDigest([]string{"GH_TOKEN"}), "db": EnvKeyDigest([]string{"DB_PASSWORD"})}
	canon := `{"env_keys":{"db":"` + digests["db"] + `","gh":"` + digests["gh"] + `"},"kind":"program","spec":{"args":["api","user"],"dir":"/srv/app","env":{"A":"1"},"env_sets":["gh"],"exec":"gh","port":null,"restart":"on-failure","run_as":"joe","secret_env":{"PGPASSWORD":"db.DB_PASSWORD"},"shell":false}}`
	if got := CanonicalProgram(withSets, digests); got != canon {
		t.Fatalf("canonical\n%s\nwant\n%s", got, canon)
	}
	sum := sha256.Sum256([]byte(canon))
	if ProgramHash(withSets, digests) != hex.EncodeToString(sum[:]) {
		t.Fatal("hash is not sha256 of the canonical form")
	}
	added := map[string]string{"gh": EnvKeyDigest([]string{"GH_TOKEN", "GH_HOST"}), "db": digests["db"]}
	if ProgramHash(withSets, added) == ProgramHash(withSets, digests) {
		t.Fatal("a key added to a set must change the hash")
	}
	inline := withSets
	inline.Env = map[string]string{"A": "2"}
	if ProgramHash(inline, digests) == ProgramHash(withSets, digests) {
		t.Fatal("inline env values stay in the hash")
	}
	if strings.Contains(CanonicalProgram(withSets, digests), "GH_TOKEN") {
		t.Fatal("key names enter only as a digest")
	}
}
