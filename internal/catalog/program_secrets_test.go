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

// api-v1 v1.22 item 2 and amendment 1: env_keys and env_values enter the
// program hash only when a set is referenced; secret values never do.
func TestProgramHashEnvKeys(t *testing.T) {
	base := Program{
		Dir: "/srv/app", Exec: "gh", Args: []string{"api", "user"}, RunAs: "joe",
		Env: map[string]string{"A": "1"}, EnvSets: []string{}, SecretEnv: map[string]string{}, Restart: "on-failure",
	}
	if ProgramHash(base, SetState{Keys: map[string][]string{"gh": {"X"}}}) != SpecHash(base) {
		t.Fatal("an entry without sets must keep its v1.6 hash")
	}
	withSets := base
	withSets.EnvSets = []string{"gh"}
	withSets.SecretEnv = map[string]string{"PGPASSWORD": "db.DB_PASSWORD"}
	// gh: GH_TOKEN secret, GH_HOST plain. db: DB_PASSWORD secret, DB_HOST plain
	// but not a secret_env target, so it does not take part.
	state := SetState{
		Keys:  map[string][]string{"gh": {"GH_HOST", "GH_TOKEN"}, "db": {"DB_HOST", "DB_PASSWORD"}},
		Plain: map[string]map[string]string{"gh": {"GH_HOST": "github.com"}, "db": {"DB_HOST": "db1"}},
	}
	keys := map[string]string{"gh": EnvKeyDigest([]string{"GH_HOST", "GH_TOKEN"}), "db": EnvKeyDigest([]string{"DB_HOST", "DB_PASSWORD"})}
	vals := map[string]string{"gh": EnvValueDigest(map[string]string{"GH_HOST": "github.com"}), "db": EnvValueDigest(nil)}
	ghSum := sha256.Sum256([]byte("GH_HOST=github.com"))
	if vals["gh"] != hex.EncodeToString(ghSum[:]) {
		t.Fatal("env_values digest is sha256 of the KEY=value lines")
	}
	canon := `{"env_keys":{"db":"` + keys["db"] + `","gh":"` + keys["gh"] + `"},"env_values":{"db":"` + vals["db"] + `","gh":"` + vals["gh"] + `"},"kind":"program","spec":{"args":["api","user"],"dir":"/srv/app","env":{"A":"1"},"env_sets":["gh"],"exec":"gh","port":null,"restart":"on-failure","run_as":"joe","secret_env":{"PGPASSWORD":"db.DB_PASSWORD"},"shell":false}}`
	if got := CanonicalProgram(withSets, state); got != canon {
		t.Fatalf("canonical\n%s\nwant\n%s", got, canon)
	}
	sum := sha256.Sum256([]byte(canon))
	if ProgramHash(withSets, state) != hex.EncodeToString(sum[:]) {
		t.Fatal("hash is not sha256 of the canonical form")
	}
	hash := ProgramHash(withSets, state)
	added := state
	added.Keys = map[string][]string{"gh": {"GH_HOST", "GH_TOKEN", "NEW"}, "db": state.Keys["db"]}
	if ProgramHash(withSets, added) == hash {
		t.Fatal("a key added to a set must change the hash")
	}
	plainChange := state
	plainChange.Plain = map[string]map[string]string{"gh": {"GH_HOST": "ghe.example"}, "db": {"DB_HOST": "db1"}}
	if ProgramHash(withSets, plainChange) == hash {
		t.Fatal("a plain taking-part value change must change the hash")
	}
	notTaking := state
	notTaking.Plain = map[string]map[string]string{"gh": {"GH_HOST": "github.com"}, "db": {"DB_HOST": "db2"}}
	if ProgramHash(withSets, notTaking) != hash {
		t.Fatal("a plain key that does not take part must not change the hash")
	}
	inline := withSets
	inline.Env = map[string]string{"A": "2"}
	if ProgramHash(inline, state) == hash {
		t.Fatal("inline env values stay in the hash")
	}
	if c := CanonicalProgram(withSets, state); strings.Contains(c, "GH_TOKEN") || strings.Contains(c, "github.com") {
		t.Fatal("key names and values enter only as digests")
	}
}

// api-v1 v1.22 amendment 1: which plain keys take part.
func TestPlainTakingPart(t *testing.T) {
	refs := EnvRefs{Sets: []string{"a"}, Secret: map[string]string{"X": "b.B1", "Y": "b.SECRET"}}
	plain := map[string]map[string]string{"a": {"A1": "1", "A2": "2"}, "b": {"B1": "3", "B2": "4"}, "c": {"C": "5"}}
	got := refs.PlainTakingPart(plain)
	if len(got) != 2 || len(got["a"]) != 2 || len(got["b"]) != 1 || got["b"]["B1"] != "3" {
		t.Fatalf("taking part %#v", got)
	}
}

// api-v1 v1.22 amendments 2 and 3: the shared helpers.
func TestSecretValueRules(t *testing.T) {
	for _, k := range []string{"NODE_OPTIONS", "LD_PRELOAD", "GIT_SSH_COMMAND", "SSL_CERT_FILE", "HTTPS_PROXY", "https_proxy", "No_Proxy", "DOTNET_STARTUP_HOOKS"} {
		if !ChangesWhatRunsSecret(k) {
			t.Fatalf("%s not in the extended list", k)
		}
	}
	for _, k := range []string{"GH_TOKEN", "HTTP_PROXY_USER", "PATHS"} {
		if ChangesWhatRunsSecret(k) {
			t.Fatalf("%s wrongly in the extended list", k)
		}
	}
	if !LooksSecret("API_TOKEN") || !LooksSecret("db_password") || LooksSecret("GH_HOST") {
		t.Fatal("LooksSecret")
	}
	// Inline env and validateEnv use the same rule.
	if _, err := BuildProgram(ProgramInput{Command: "x", Dir: "/srv", RunAs: "joe", Env: map[string]string{"API_TOKEN": "v"}}); err == nil || err.Error() != "use secret_env" {
		t.Fatalf("inline %v", err)
	}
	// The extended list is refused for secret_env on write; inline env keeps v1.6.1.
	known := SetKeys{"s": {"V"}}
	if _, err := BuildProgram(ProgramInput{Command: "x", Dir: "/srv", RunAs: "joe", SecretEnv: map[string]string{"GIT_SSH_COMMAND": "s.V"}, Sets: known}); err == nil || err.Error() != "this variable changes what the program runs" {
		t.Fatalf("secret_env GIT_SSH_COMMAND %v", err)
	}
	if _, err := BuildProgram(ProgramInput{Command: "x", Dir: "/srv", RunAs: "joe", Env: map[string]string{"GIT_SSH_COMMAND": "ssh -i k"}}); err != nil {
		t.Fatalf("inline GIT_SSH_COMMAND must stay allowed: %v", err)
	}
}
