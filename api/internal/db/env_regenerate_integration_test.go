package db

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// assertEnvRegenerate pins PutEnvKey with Regenerate (api-v1 v1.54, v1.56):
// it replaces an existing generated secret value with a pending generated
// one; a hand-entered (typed) secret is ErrNotGenerated and keeps its value;
// a plain key is ErrSecretFixed; a missing key or set is ErrNotFound and
// creates nothing; a private set "@<entry>" is accepted.
func assertEnvRegenerate(t *testing.T, d *DB, ctx context.Context, at time.Time) {
	t.Helper()
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "env-regenerate-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "EnvRegenerate", "envregen"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pid := home.ID.String()
	put := func(set, key, plain string, in store.EnvKeyPut) (store.EnvKey, error) {
		t.Helper()
		nonce, ciphertext, err := box.Seal(seal.EnvAAD(pid, set, key), []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		in.Key, in.Plain, in.Nonce, in.Ciphertext, in.KeyVersion = key, plain, nonce, ciphertext, seal.KeyVersion
		got, _, err := d.PutEnvKey(ctx, home.ID, set, in, owner.ID, at)
		return got, err
	}
	open := func(key, want string) {
		t.Helper()
		mat, err := d.RevealEnvKey(ctx, home.ID, "svc", key)
		if err != nil {
			t.Fatal(err)
		}
		if opened, err := box.Open(seal.EnvAAD(pid, "svc", key), mat.Nonce, mat.Ciphertext); err != nil || string(opened) != want {
			t.Fatalf("reveal %s %v", key, err)
		}
	}
	no := false
	if _, err := put("svc", "TYPED", "typed-secret-value", store.EnvKeyPut{}); err != nil {
		t.Fatal(err)
	}
	if _, err := put("svc", "GEN", "generated-zero", store.EnvKeyPut{Generated: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := put("svc", "PLAIN", "visible", store.EnvKeyPut{Secret: &no}); err != nil {
		t.Fatal(err)
	}
	regen := store.EnvKeyPut{Generated: true, Regenerate: true}
	// v1.56: a typed secret is never replaced by a random value.
	if _, err := put("svc", "TYPED", "nope", regen); !errors.Is(err, store.ErrNotGenerated) {
		t.Fatalf("typed regenerate %v", err)
	}
	set, err := d.GetEnvSet(ctx, home.ID, "svc")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range set.Keys {
		if k.Key == "TYPED" && (k.Generated || k.RevealPending) {
			t.Fatalf("typed key changed %#v", k)
		}
	}
	open("GEN", "generated-zero")
	got, err := put("svc", "GEN", "regenerated-one", regen)
	if err != nil || !got.RevealPending || !got.Generated || !got.IsSecret {
		t.Fatalf("regenerate %#v %v", got, err)
	}
	open("GEN", "regenerated-one")
	// Regenerating again after the reveal works: the forgotten-value case.
	if got, err := put("svc", "GEN", "regenerated-two", regen); err != nil || !got.RevealPending {
		t.Fatalf("second regenerate %#v %v", got, err)
	}
	if _, err := put("svc", "PLAIN", "nope", regen); !errors.Is(err, store.ErrSecretFixed) {
		t.Fatalf("plain regenerate %v", err)
	}
	if _, err := put("svc", "GONE", "nope", regen); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing key %v", err)
	}
	if _, err := put("other", "TYPED", "nope", regen); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing set %v", err)
	}
	if _, err := d.GetEnvSet(ctx, home.ID, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused regenerate created the set: %v", err)
	}
	// Ordinary puts still refuse a private set name.
	if _, err := put("@web", "KEY", "x", store.EnvKeyPut{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("private plain put %v", err)
	}
}
