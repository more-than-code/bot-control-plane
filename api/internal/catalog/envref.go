package catalog

import (
	"sort"
	"strings"
)

// RefError refuses a deploy or rollback whose env has a bad ${KEY}
// reference (api-v1 v1.27 item 7), or resolves a placeholder that has no
// value yet (item 6). Msg names keys and sets only, never a value. The API
// answers it as bad_request.
type RefError struct {
	Msg string
}

func (e *RefError) Error() string { return e.Msg }

// Resolved is the result of ResolveEnvRefs. Refs lists, for each key whose
// value held a reference, the names it referenced, in order of appearance.
type Resolved struct {
	Env  map[string]string
	Refs map[string][]string
}

// Spread returns a copy of flags in which every key that references a
// flagged key is flagged too. One level only, as resolution is one pass: a
// referenced value is inserted as stored, so its own references carry nothing.
func (r Resolved) Spread(flags map[string]bool) map[string]bool {
	out := make(map[string]bool, len(flags))
	for key, on := range flags {
		out[key] = on
	}
	for key, names := range r.Refs {
		for _, name := range names {
			if flags[name] {
				out[key] = true
				break
			}
		}
	}
	return out
}

// ResolveEnvRefs resolves ${NAME} references in a merged image env, in one
// pass (api-v1 v1.27 item 7):
//   - ${NAME}, where NAME matches the env key rule, becomes the merged value
//     of NAME as stored; a ${ inside that value is not scanned again;
//   - $${ is a literal ${;
//   - when the text after ${ up to the next } is not a valid key name, those
//     two characters ${ are literal and scanning continues right after them,
//     so in ${A${B} the inner ${B} is resolved;
//   - any other $ is literal;
//   - an unknown NAME, a ${ with no } after it (even if the name would be
//     invalid), or a result over 4096 bytes is a *RefError.
//
// secret maps each key to whether its value is secret. For a secret key the
// unknown-name message does not echo NAME. The inputs are not changed. Keys
// are checked in byte order, so the reported error is stable.
func ResolveEnvRefs(env map[string]string, secret map[string]bool) (Resolved, error) {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := Resolved{Env: make(map[string]string, len(env)), Refs: map[string][]string{}}
	for _, key := range keys {
		val, refs, err := resolveValue(key, env[key], env, secret[key])
		if err != nil {
			return Resolved{}, err
		}
		if len(val) > maxEnvValue {
			return Resolved{}, &RefError{Msg: "env " + key + " is longer than 4096 bytes after references"}
		}
		out.Env[key] = val
		if len(refs) > 0 {
			out.Refs[key] = refs
		}
	}
	return out, nil
}

func resolveValue(key, val string, env map[string]string, keySecret bool) (string, []string, error) {
	if !strings.Contains(val, "${") {
		return val, nil, nil
	}
	var b strings.Builder
	var refs []string
	for i := 0; i < len(val); {
		if strings.HasPrefix(val[i:], "$${") {
			b.WriteString("${")
			i += 3
			continue
		}
		if !strings.HasPrefix(val[i:], "${") {
			b.WriteByte(val[i])
			i++
			continue
		}
		end := strings.IndexByte(val[i+2:], '}')
		if end < 0 {
			return "", nil, &RefError{Msg: "env " + key + " has an unterminated ${"}
		}
		name := val[i+2 : i+2+end]
		if !envKeyRe.MatchString(name) {
			b.WriteString("${")
			i += 2
			continue
		}
		ref, ok := env[name]
		if !ok {
			if keySecret {
				return "", nil, &RefError{Msg: "env " + key + " has a reference that is not set for this entry"}
			}
			return "", nil, &RefError{Msg: "env " + key + " references ${" + name + "}, which is not set for this entry"}
		}
		b.WriteString(ref)
		refs = append(refs, name)
		i += 2 + end + 1
	}
	return b.String(), refs, nil
}
