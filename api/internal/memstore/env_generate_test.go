package memstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestRevealEnvKey mirrors the db assertion assertEnvGenerate (api-v1 v1.27).
func TestRevealEnvKey(t *testing.T) {
	ctx := context.Background()
	m := New()
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	owner := catalogUser(t, m, "gen-owner")
	home := catalogProject(t, m, owner.ID, "Gen", "gen", at)
	box, err := seal.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pid := home.ID.String()
	// put with generated drops PW first: generate makes new keys only (rev 3).
	put := func(plain string, generated bool) store.EnvKey {
		t.Helper()
		if generated {
			if err := m.DeleteEnvKey(ctx, home.ID, "svc", "PW", owner.ID, at); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
		}
		s := sealKey(t, box, pid, "svc", "PW", plain)
		got, _, err := m.PutEnvKey(ctx, home.ID, "svc", store.EnvKeyPut{Key: "PW", Plain: plain, Nonce: s.Nonce, Ciphertext: s.Ciphertext, KeyVersion: s.KeyVersion, Generated: generated}, owner.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	sealedPlain := func(plain string) store.EnvSecret {
		s := sealKey(t, box, pid, "svc", "PW", plain)
		s.Plain = plain
		return s
	}
	generated := func() bool {
		set, err := m.GetEnvSet(ctx, home.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		mats, err := m.ListEnvMaterial(ctx, home.ID, "svc")
		if err != nil || mats[0].Generated != set.Keys[0].Generated {
			t.Fatalf("view and material disagree %v", err)
		}
		return set.Keys[0].Generated
	}
	pending := func() bool {
		set, err := m.GetEnvSet(ctx, home.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		return set.Keys[0].RevealPending
	}
	if got := put("one", true); !got.RevealPending || !got.Generated || !pending() || !generated() {
		t.Fatal("generated put not pending and generated")
	}
	mat, err := m.RevealEnvKey(ctx, home.ID, "svc", "PW")
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := box.Open(seal.EnvAAD(pid, "svc", "PW"), mat.Nonce, mat.Ciphertext); err != nil || string(opened) != "one" {
		t.Fatalf("reveal did not open %v", err)
	}
	if _, err := m.RevealEnvKey(ctx, home.ID, "svc", "PW"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second %v", err)
	}
	if !mat.Generated || !generated() {
		t.Fatal("reveal cleared generated")
	}
	if _, err := m.RevealEnvKey(ctx, home.ID, "svc", "GONE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing key %v", err)
	}
	if _, err := m.RevealEnvKey(ctx, home.ID, "nope", "PW"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing set %v", err)
	}
	put("two", true)
	if got := put("typed", false); got.RevealPending || got.Generated || pending() || generated() {
		t.Fatal("typed put left pending or generated")
	}
	put("three", true)
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	wins := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := m.RevealEnvKey(ctx, home.ID, "svc", "PW"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins %d", wins)
	}
	put("four", true)
	if _, _, err := m.MergeEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealedPlain("merged")}, owner.ID, at, true); err != nil || pending() {
		t.Fatalf("merge left pending %v", err)
	}
	put("five", true)
	if _, err := m.PutEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealedPlain("replaced")}, owner.ID, at, true); err != nil || pending() || generated() {
		t.Fatalf("replace left pending %v", err)
	}

	// needs_value parity with the db store: every value write clears it.
	// Phase B placeholders do not exist yet, so set the flag on the record.
	setNeeds := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		id := envKey(home.ID, "svc")
		m.envValues[id][0].NeedsValue = true
	}
	needs := func() bool {
		set, err := m.GetEnvSet(ctx, home.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		return set.Keys[0].NeedsValue
	}
	for _, write := range []struct {
		name string
		do   func() error
	}{
		{"typed put", func() error { put("typed-again", false); return nil }},
		{"merge", func() error {
			_, _, err := m.MergeEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealedPlain("merged-2")}, owner.ID, at, true)
			return err
		}},
		{"replace", func() error {
			_, err := m.PutEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealedPlain("replaced-2")}, owner.ID, at, true)
			return err
		}},
	} {
		setNeeds()
		if !needs() {
			t.Fatal("flag not set")
		}
		if err := write.do(); err != nil || needs() {
			t.Fatalf("%s left needs_value %v", write.name, err)
		}
	}

	// Make secret keeps it on a plain placeholder: it is not a value write
	// (v1.27 item 2), so an empty secret never deploys.
	if _, err := m.PutEnvSet(ctx, home.ID, "plainset", []store.EnvSecret{{Key: "P", Plain: "plain"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.envValues[envKey(home.ID, "plainset")][0].NeedsValue = true
	m.mu.Unlock()
	sealer := func(key, plain string) (store.EnvSecret, error) {
		return sealKey(t, box, pid, "plainset", key, plain), nil
	}
	if set, changed, err := m.MakeEnvKeysSecret(ctx, home.ID, "plainset", []string{"P"}, sealer, owner.ID, at); err != nil || len(changed) != 1 || !set.Keys[0].NeedsValue || set.Keys[0].Generated {
		t.Fatalf("make secret flags %#v %v", set.Keys, err)
	}

	// Generate over an existing secret or plain key is ErrKeyExists and
	// writes nothing (rev 3); a needs_value placeholder is the exception (TestParityEnvSetCreate).
	put("seven", true)
	snap := func() string {
		mats, _ := m.ListEnvMaterial(ctx, home.ID, "svc")
		set, _ := m.GetEnvSet(ctx, home.ID, "svc")
		return string(mats[0].Ciphertext) + fmt.Sprint(set.Keys, set.UpdatedAt)
	}
	before := snap()
	over := sealKey(t, box, pid, "svc", "PW", "would-replace")
	if _, _, err := m.PutEnvKey(ctx, home.ID, "svc", store.EnvKeyPut{Key: "PW", Plain: "would-replace", Nonce: over.Nonce, Ciphertext: over.Ciphertext, KeyVersion: over.KeyVersion, Generated: true}, owner.ID, at.Add(time.Hour)); !errors.Is(err, store.ErrKeyExists) || snap() != before {
		t.Fatalf("generate over secret %v", err)
	}
	if _, err := m.PutEnvSet(ctx, home.ID, "plain2", []store.EnvSecret{{Key: "P", Plain: "visible"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.PutEnvKey(ctx, home.ID, "plain2", store.EnvKeyPut{Key: "P", Plain: "x", Generated: true}, owner.ID, at); !errors.Is(err, store.ErrKeyExists) {
		t.Fatalf("generate over plain %v", err)
	}
	if set, err := m.GetEnvSet(ctx, home.ID, "plain2"); err != nil || set.Keys[0].IsSecret || *set.Keys[0].Value != "visible" {
		t.Fatalf("plain key changed %v", err)
	}

	// A deleted project's keys cannot be revealed.
	put("six", true)
	// Mark only the project deleted, so the set and key are still there.
	m.mu.Lock()
	proj := m.projects[home.ID]
	proj.DeletedAt = &at
	m.projects[home.ID] = proj
	m.mu.Unlock()
	if _, err := m.RevealEnvKey(ctx, home.ID, "svc", "PW"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted project reveal %v", err)
	}
}
