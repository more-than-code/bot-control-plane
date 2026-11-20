package catalog

import "testing"

// pathKeyCases is the path-exemption case table (api-v1 v1.39 item 4). The
// same table is in the console's tests/catalog-image.test.ts
// (PATH_KEY_CASES); keep both in step.
var pathKeyCases = []struct {
	key  string
	want string // "refused", "exempt" (secret-looking, a path name) or "plain"
}{
	{"API_KEY", "refused"},
	{"API_KEY_FILE", "exempt"},
	{"X_PRIVATE_KEY_FILE", "exempt"},
	{"GITHUB_TOKEN_PATH", "exempt"},
	{"SECRETS_DIR", "exempt"},
	{"PASSWORD_DIR", "exempt"},
	{"FILE_TOKEN", "refused"},
	{"PASSWORD_DIRECTORY", "refused"},
	{"TOKEN_FILENAME", "refused"},
	{"SECRET_FILE_X", "refused"},
	{"_FILE", "plain"},
	{"PATH_ONLY_FILE", "plain"},
}

// api-v1 v1.39 item 4: a secret-looking inline key whose name ends in _FILE,
// _PATH or _DIR holds a path and is not refused; any other is refused with a
// message that names the key.
func TestInlineEnvPathKeys(t *testing.T) {
	const msg = " looks like a secret: put it in secret_env or in a secret env-set key"
	for _, tc := range pathKeyCases {
		if LooksSecret(tc.key) != (tc.want != "plain") || LooksSecretNotPath(tc.key) != (tc.want == "refused") {
			t.Fatalf("%s: rule disagrees with %s", tc.key, tc.want)
		}
		env := map[string]string{tc.key: "/etc/x.pem"}
		_, perr := BuildProgram(ProgramInput{Command: "x", Dir: "/srv", RunAs: "joe", Env: env})
		_, ierr := BuildImage("web", []byte(`{"kind":"service","image":"nginx","env":{"`+tc.key+`":"/etc/x.pem"}}`), nil, nil)
		for kind, err := range map[string]error{"program": perr, "image": ierr} {
			if tc.want == "refused" {
				if err == nil || err.Error() != tc.key+msg {
					t.Fatalf("%s %s: %v", kind, tc.key, err)
				}
			} else if err != nil {
				t.Fatalf("%s %s: %v", kind, tc.key, err)
			}
		}
	}
	// The suffix rule ignores case.
	if LooksSecretNotPath("x_private_key_file") || LooksSecretNotPath("Token_Path") || !LooksSecretNotPath("db_password") {
		t.Fatal("LooksSecretNotPath case")
	}
	// Several secret-looking keys: the refusal names the first by name, every time.
	for i := 0; i < 20; i++ {
		_, err := BuildProgram(ProgramInput{Command: "x", Dir: "/srv", RunAs: "joe", Env: map[string]string{"Z_TOKEN": "a", "A_SECRET": "b", "M_PASSWORD": "c"}})
		if err == nil || err.Error() != "A_SECRET"+msg {
			t.Fatalf("order: %v", err)
		}
	}
}
