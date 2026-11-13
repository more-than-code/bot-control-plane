package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	CodeBadRequest     = "bad_request"
	CodeNotImplemented = "not_implemented"
	restartOnFailure   = "on-failure"
	restartNo          = "no"
)

var (
	envKeyRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	bareExecRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	absExecRe  = regexp.MustCompile(`^/[A-Za-z0-9._+/-]{1,255}$`)
	runAsRe    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	secretKey  = regexp.MustCompile(`(?i)(password|passwd|token|secret|credential|api[_-]?key|private[_-]?key)`)
	safeWord   = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,/-]+$`)
	idemKeyRe  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// MaxProgramEnv is the most env keys a program may have, inline and after
// the env-set merge (api-v1 v1.6 rule 8, v1.22 item 1).
const MaxProgramEnv = 64

// FieldError is one v1.6 validation failure. Code is an envelope code.
type FieldError struct {
	Code string
	Msg  string
}

func (e *FieldError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

func fieldErr(msg string) error {
	return &FieldError{Code: CodeBadRequest, Msg: msg}
}

func notImplemented(msg string) error {
	return &FieldError{Code: CodeNotImplemented, Msg: msg}
}

// Program is a normalized program spec. EnvSets and SecretEnv (api-v1
// v1.22) are non-nil after BuildProgram.
type Program struct {
	Dir       string
	Exec      string
	Args      []string
	Shell     bool
	RunAs     string
	Env       map[string]string
	EnvSets   []string
	SecretEnv map[string]string
	Port      *int
	Restart   string
	Display   string
}

// ProgramInput is the create and replace body after the command is split.
type ProgramInput struct {
	Command string
	Shell   bool
	Dir     string
	RunAs   string
	Env     map[string]string
	Port    *int
	Restart string // empty means the default
	// EnvSets and SecretEnv follow the image rules 6 and 7 (api-v1 v1.22).
	// Sets holds the project's env sets and their key names.
	EnvSets   []string
	SecretEnv map[string]string
	Sets      SetKeys
}

// SetState is a project's env sets as a program spec_hash sees them: the key
// names of each set, and the values of its keys with is_secret false.
type SetState struct {
	Keys  map[string][]string          // set -> key names
	Plain map[string]map[string]string // set -> plain key -> value
}

// BuildProgram applies v1.6 rules 1–9, and the image env-set rules 6, 7 and
// the 64-key merge limit (api-v1 v1.22 item 1), and returns the stored spec.
// A non-program kind is refused by the caller (rule 10).
func BuildProgram(in ProgramInput) (Program, error) {
	split, err := Split(in.Command, in.Shell)
	if err != nil {
		return Program{}, err
	}
	env, err := mergeEnv(in.Env, split.Env)
	if err != nil {
		return Program{}, err
	}
	if err := validateExec(split.Exec); err != nil {
		return Program{}, err
	}
	if err := validateArgs(split.Args); err != nil {
		return Program{}, err
	}
	if err := validateDir(in.Dir); err != nil {
		return Program{}, err
	}
	if err := validateRunAs(in.RunAs); err != nil {
		return Program{}, err
	}
	if err := validateEnv(env); err != nil {
		return Program{}, err
	}
	envSets := in.EnvSets
	if envSets == nil {
		envSets = []string{}
	}
	if err := validateEnvSets(envSets, in.Sets); err != nil {
		return Program{}, err
	}
	secretEnv := map[string]string{}
	for k, v := range in.SecretEnv {
		secretEnv[k] = v
	}
	if len(secretEnv) > maxSecretEnv {
		return Program{}, fieldErr("invalid secret_env")
	}
	if err := validateSecretEnv(secretEnv, in.Sets); err != nil {
		return Program{}, err
	}
	for k := range secretEnv {
		if ChangesWhatRunsSecret(k) {
			return Program{}, fieldErr("this variable changes what the program runs")
		}
	}
	refs := EnvRefs{Sets: envSets, Secret: secretEnv, Inline: env}
	if mergedKeyCount(refs, in.Sets) > MaxProgramEnv {
		return Program{}, fieldErr("too many env keys")
	}
	if in.Port != nil && (*in.Port < 1 || *in.Port > 65535) {
		return Program{}, fieldErr("invalid port")
	}
	restart := in.Restart
	if restart == "" {
		restart = restartOnFailure
	}
	if restart != restartOnFailure && restart != restartNo {
		return Program{}, fieldErr("invalid restart")
	}
	if split.Args == nil {
		split.Args = []string{}
	}
	return Program{
		Dir:       in.Dir,
		Exec:      split.Exec,
		Args:      split.Args,
		Shell:     split.Shell,
		RunAs:     in.RunAs,
		Env:       env,
		EnvSets:   envSets,
		SecretEnv: secretEnv,
		Port:      in.Port,
		Restart:   restart,
		Display:   split.Display,
	}, nil
}

// Preview applies rules 1–5 and returns the form preview.
func Preview(command string, shell bool) (SplitResult, error) {
	split, err := Split(command, shell)
	if err != nil {
		return SplitResult{}, err
	}
	if err := validateExec(split.Exec); err != nil {
		return SplitResult{}, err
	}
	if err := validateArgs(split.Args); err != nil {
		return SplitResult{}, err
	}
	return split, nil
}

func mergeEnv(body, hoisted map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range body {
		out[k] = v
	}
	for k, v := range hoisted {
		if _, exists := out[k]; exists {
			return nil, fieldErr("duplicate env " + k)
		}
		out[k] = v
	}
	return out, nil
}

func validateExec(exec string) error {
	if strings.HasPrefix(exec, "/") {
		if !absExecRe.MatchString(exec) || hasDotDot(exec) {
			return fieldErr("invalid exec")
		}
		return nil
	}
	if !bareExecRe.MatchString(exec) {
		return fieldErr("invalid exec")
	}
	return nil
}

func validateArgs(args []string) error {
	if len(args) > 32 {
		return fieldErr("too many args")
	}
	for _, a := range args {
		if len(a) > 4096 {
			return fieldErr("arg is too long")
		}
		if strings.ContainsAny(a, "\n\r") || strings.ContainsRune(a, 0) {
			return fieldErr("invalid arg")
		}
	}
	return nil
}

func validateDir(dir string) error {
	if dir == "" || len(dir) > 255 || !strings.HasPrefix(dir, "/") {
		return fieldErr("invalid dir")
	}
	if strings.ContainsRune(dir, 0) || strings.ContainsAny(dir, "\n\r") || hasDotDot(dir) {
		return fieldErr("invalid dir")
	}
	return nil
}

func hasDotDot(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func validateRunAs(user string) error {
	if user == "root" || !runAsRe.MatchString(user) {
		return fieldErr("invalid run_as")
	}
	return nil
}

// ValidateEnvKey applies the dotenv key rules without a line number.
// The message may name the key. It never includes a value.
func ValidateEnvKey(key string) error {
	if !envKeyRe.MatchString(key) {
		return fieldErr("invalid env key")
	}
	if key == "PATH" || key == "HOME" {
		return fieldErr(key + " is reserved")
	}
	if changesWhatRuns(key) {
		return fieldErr("this variable changes what the program runs")
	}
	return nil
}

// ValidateEnvValue applies the dotenv value rules. The message does not
// include the value. An empty value is valid.
func ValidateEnvValue(value string) error {
	if strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\n\r") || len(value) > maxEnvValue {
		return fieldErr("invalid env value")
	}
	return nil
}

func validateEnv(env map[string]string) error {
	if len(env) > MaxProgramEnv {
		return fieldErr("too many env keys")
	}
	for k, v := range env {
		if !envKeyRe.MatchString(k) {
			return fieldErr("invalid env key")
		}
		if k == "PATH" || k == "HOME" {
			return fieldErr(k + " is reserved")
		}
		if LooksSecret(k) {
			return fieldErr("use secret_env")
		}
		if changesWhatRuns(k) {
			return fieldErr("this variable changes what the program runs")
		}
		if len(v) > 4096 || strings.ContainsAny(v, "\n\r") || strings.ContainsRune(v, 0) {
			return fieldErr("invalid env value")
		}
	}
	return nil
}

// ChangesWhatRunsSecret is api-v1 v1.22 amendment 2: the v1.6.1 list plus
// the keys a secret value may not set on a program, because a secret value
// is not shown in the host owner's approval. The proxy keys match in either
// case.
func ChangesWhatRunsSecret(key string) bool {
	if changesWhatRuns(key) {
		return true
	}
	switch key {
	case "GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS",
		"PYTHONHOME", "PYTHONINSPECT", "GCONV_PATH", "DOTNET_STARTUP_HOOKS",
		"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	}
	switch strings.ToUpper(key) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	return false
}

// LooksSecret is the inline-env secret-looking rule ("use secret_env",
// api-v1 v1.6 rule 8). The Run gate for plain set keys (v1.22 amendment 3)
// uses the same rule.
func LooksSecret(key string) bool {
	return secretKey.MatchString(key)
}

// changesWhatRuns is api-v1 v1.6.1 item 2. The same keys are refused when
// they arrive as hoisted KEY=value words, because those words are merged
// into env before this check.
func changesWhatRuns(key string) bool {
	if strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
		return true
	}
	switch key {
	case "NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "ENV", "IFS",
		"PYTHONPATH", "PYTHONSTARTUP", "PERL5OPT", "PERL5LIB", "RUBYOPT", "RUBYLIB":
		return true
	default:
		return false
	}
}

// ValidName reports whether name is a catalog entry name.
func ValidName(name string) bool {
	return nameRe.MatchString(name)
}

// ValidIdempotencyKey reports whether key matches the v1.6 key rule.
func ValidIdempotencyKey(key string) bool {
	return idemKeyRe.MatchString(key)
}

// Canonical is the v1.6 spec_hash input: {"kind","spec"} with sorted keys and
// no whitespace. It is the whole input for an entry without env sets.
func Canonical(p Program) string {
	return `{"kind":"program","spec":` + programSpecJSON(p, false) + `}`
}

// CanonicalProgram is the spec_hash input (api-v1 v1.22 item 2 and amendment
// 1). An entry that references an env set adds env_sets and secret_env to
// spec, and beside it env_keys {<set>: EnvKeyDigest} as images do, and
// env_values {<set>: EnvValueDigest} of the set's plain taking-part keys.
// Secret values never enter it; inline env values do. An entry without sets
// keeps the v1.6 input, so its hash and host approvals do not change.
func CanonicalProgram(p Program, sets SetState) string {
	refs := p.EnvRefs()
	if !refs.Any() {
		return Canonical(p)
	}
	names := refs.SetNames()
	values := refs.PlainTakingPart(sets.Plain)
	valueDigests := make(map[string]string, len(names))
	for _, set := range names {
		valueDigests[set] = EnvValueDigest(values[set])
	}
	return `{"env_keys":` + envKeysJSON(names, KeyDigests(names, sets.Keys)) +
		`,"env_values":` + envKeysJSON(names, valueDigests) +
		`,"kind":"program","spec":` + programSpecJSON(p, true) + `}`
}

func programSpecJSON(p Program, withRefs bool) string {
	envKeys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	var env strings.Builder
	env.WriteByte('{')
	for i, k := range envKeys {
		if i > 0 {
			env.WriteByte(',')
		}
		env.WriteString(jsonString(k))
		env.WriteByte(':')
		env.WriteString(jsonString(p.Env[k]))
	}
	env.WriteByte('}')
	var args strings.Builder
	args.WriteByte('[')
	for i, a := range p.Args {
		if i > 0 {
			args.WriteByte(',')
		}
		args.WriteString(jsonString(a))
	}
	args.WriteByte(']')
	port := "null"
	if p.Port != nil {
		port = strconv.Itoa(*p.Port)
	}
	shell := "false"
	if p.Shell {
		shell = "true"
	}
	if !withRefs {
		return `{"args":` + args.String() +
			`,"dir":` + jsonString(p.Dir) +
			`,"env":` + env.String() +
			`,"exec":` + jsonString(p.Exec) +
			`,"port":` + port +
			`,"restart":` + jsonString(p.Restart) +
			`,"run_as":` + jsonString(p.RunAs) +
			`,"shell":` + shell + `}`
	}
	return `{"args":` + args.String() +
		`,"dir":` + jsonString(p.Dir) +
		`,"env":` + env.String() +
		`,"env_sets":` + stringArrayJSON(p.EnvSets) +
		`,"exec":` + jsonString(p.Exec) +
		`,"port":` + port +
		`,"restart":` + jsonString(p.Restart) +
		`,"run_as":` + jsonString(p.RunAs) +
		`,"secret_env":` + stringMapJSON(p.SecretEnv) +
		`,"shell":` + shell + `}`
}

// SpecHash is the spec_hash of a program without env sets (Canonical).
func SpecHash(p Program) string {
	return ProgramHash(p, SetState{})
}

// ProgramHash is the program spec_hash over the project's current sets. A
// set missing from sets hashes as empty.
func ProgramHash(p Program, sets SetState) string {
	sum := sha256.Sum256([]byte(CanonicalProgram(p, sets)))
	return hex.EncodeToString(sum[:])
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// RejectKind reports rule 10. A missing kind is not this error.
// kind image is accepted. Top-level secret_env is a program field (api-v1
// v1.22 item 1); on any other body it is still not implemented. Image files
// are a spec field, not a kind.
func RejectKind(kind string, secretEnv bool) error {
	if (secretEnv && kind != "program") || (kind != "" && kind != "program" && kind != "image") {
		return notImplemented("not implemented")
	}
	return nil
}
