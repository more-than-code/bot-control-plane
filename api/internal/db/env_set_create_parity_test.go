package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestParityEnvSetCreate is api-v1 v1.27 items 4 and 6 in both stores: the
// confirmation table (put, get, expiry, owner, single use, a conflict that
// keeps the plan), placeholders and generated keys, needs_value in the
// material, and generate filling a placeholder.
func TestParityEnvSetCreate(t *testing.T) {
	runParity(t, func(p parity) {
		owner, other := p.user("eo"), p.user("ex")
		pr := p.project(p.org(owner.ID), owner.ID, "envc")
		box, err := seal.New(bytes.Repeat([]byte{5}, 32))
		if err != nil {
			p.t.Fatal(err)
		}
		sealed := func(set, key, plain string) store.EnvKeyCreate {
			p.t.Helper()
			nonce, ct, err := box.Seal(seal.EnvAAD(pr.ID.String(), set, key), []byte(plain))
			if err != nil {
				p.t.Fatal(err)
			}
			return store.EnvKeyCreate{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}
		}
		keys := func(set string) []store.EnvKeyCreate {
			gen := sealed(set, "GEN", "generated-value-0123456789")
			gen.Generated, gen.Secret = true, true
			ph := sealed(set, "PH", "")
			ph.Secret = true
			return []store.EnvKeyCreate{gen, ph, {Key: "PLAIN"}}
		}

		// Console create (no plan).
		set, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "direct", keys("direct"), nil, owner.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		flags := map[string][3]bool{}
		for _, k := range set.Keys {
			flags[k.Key] = [3]bool{k.IsSecret, k.RevealPending, k.NeedsValue}
		}
		if flags["GEN"] != [3]bool{true, true, false} || flags["PH"] != [3]bool{true, false, true} || flags["PLAIN"] != [3]bool{false, false, true} {
			p.t.Fatalf("flags %v", flags)
		}
		mats, err := p.st.ListEnvMaterial(p.ctx, pr.ID, "direct")
		if err != nil {
			p.t.Fatal(err)
		}
		for _, m := range mats {
			if m.NeedsValue != (m.Key != "GEN") || m.Generated != (m.Key == "GEN") {
				p.t.Fatalf("material %s needs %v generated %v", m.Key, m.NeedsValue, m.Generated)
			}
		}
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "direct", keys("direct"), nil, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("existing set %v", err)
		}

		// Generate fills a secret placeholder once; a filled key refuses it.
		gen := func(key string) error {
			s := sealed("direct", key, "filled-generated-value-01")
			yes := true
			_, _, err := p.st.PutEnvKey(p.ctx, pr.ID, "direct", store.EnvKeyPut{Key: key, Plain: "filled-generated-value-01", Secret: &yes, Nonce: s.Nonce, Ciphertext: s.Ciphertext, KeyVersion: s.KeyVersion, Generated: true}, owner.ID, p.at)
			return err
		}
		if err := gen("PH"); err != nil {
			p.t.Fatal(err)
		}
		if err := gen("PH"); !errors.Is(err, store.ErrKeyExists) {
			p.t.Fatalf("second generate %v", err)
		}
		if err := gen("PLAIN"); !errors.Is(err, store.ErrSecretFixed) {
			p.t.Fatalf("generate on a plain placeholder %v", err)
		}
		// A typed value fills the plain placeholder.
		if _, _, err := p.st.PutEnvKey(p.ctx, pr.ID, "direct", store.EnvKeyPut{Key: "PLAIN", Plain: "smtp.example"}, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		mats, _ = p.st.ListEnvMaterial(p.ctx, pr.ID, "direct")
		for _, m := range mats {
			if m.NeedsValue {
				p.t.Fatalf("%s still needs a value", m.Key)
			}
		}

		// A plain key with its value (api-v1 v1.32 item 2): stored plain,
		// with needs_value false; an empty value is a value too.
		dsn, empty := "postgres://app:${DB_PASSWORD}@db/app", ""
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "valued", []store.EnvKeyCreate{
			{Key: "DSN", Value: &dsn}, {Key: "EMPTY", Value: &empty}, {Key: "PH"},
		}, nil, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		mats, err = p.st.ListEnvMaterial(p.ctx, pr.ID, "valued")
		if err != nil {
			p.t.Fatal(err)
		}
		byKey := map[string]store.EnvMaterial{}
		for _, m := range mats {
			byKey[m.Key] = m
		}
		if m := byKey["DSN"]; m.IsSecret || m.NeedsValue || m.Plain != dsn || m.Ciphertext != nil {
			p.t.Fatalf("DSN %+v", m)
		}
		if m := byKey["EMPTY"]; m.IsSecret || m.NeedsValue || m.Plain != "" {
			p.t.Fatalf("EMPTY %+v", m)
		}
		if m := byKey["PH"]; m.IsSecret || !m.NeedsValue {
			p.t.Fatalf("PH %+v", m)
		}

		// The plan table.
		_, hash, err := secret.NewToken()
		if err != nil {
			p.t.Fatal(err)
		}
		body := json.RawMessage(`{"keys":[{"key":"GEN","generate":{"kind":"hex"},"is_secret":true}]}`)
		plan := store.EnvSetConfirmation{
			UserID: owner.ID, ProjectID: pr.ID, SetName: "planned", Body: body,
			RequestHash: bytes.Repeat([]byte{1}, 32), IdempotencyKey: "plan-1",
			CreatedAt: p.at, ExpiresAt: p.at.Add(store.ConfirmationTTL),
		}
		if err := p.st.PutEnvSetConfirmation(p.ctx, hash, plan); err != nil {
			p.t.Fatal(err)
		}
		got, err := p.st.GetEnvSetConfirmation(p.ctx, hash)
		if err != nil || got.SetName != "planned" || got.IdempotencyKey != "plan-1" || got.UserID != owner.ID || got.UsedAt != nil || !store.SameJSON(got.Body, body) {
			p.t.Fatalf("plan %+v %v", got, err)
		}
		if err := p.st.PutEnvSetConfirmation(p.ctx, hash, plan); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("duplicate hash %v", err)
		}
		create := func(name string, actor store.User, at time.Time) error {
			_, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, name, keys(name)[:1], hash, actor.ID, at)
			return err
		}
		// Another user, another name, or after expiry: not found, plan kept.
		if err := create("planned", other, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("other user %v", err)
		}
		if err := create("elsewhere", owner, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("other name %v", err)
		}
		if err := create("planned", owner, p.at.Add(store.ConfirmationTTL+time.Second)); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("expired %v", err)
		}
		// The name taken meanwhile: conflict, and the plan stays usable.
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "planned", []store.EnvKeyCreate{{Key: "X"}}, nil, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if err := create("planned", owner, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("taken name %v", err)
		}
		if _, err := p.st.GetEnvSetConfirmation(p.ctx, hash); err != nil {
			p.t.Fatalf("plan lost after a conflict: %v", err)
		}
		if err := p.st.DeleteEnvSet(p.ctx, pr.ID, "planned"); err != nil {
			p.t.Fatal(err)
		}
		if err := create("planned", owner, p.at); err != nil {
			p.t.Fatal(err)
		}
		// Single use: the plan is marked used (api-v1 v1.32 item 1).
		if used, err := p.st.GetEnvSetConfirmation(p.ctx, hash); err != nil || used.UsedAt == nil {
			p.t.Fatalf("plan after use %+v %v", used, err)
		}
		if err := p.st.DeleteEnvSet(p.ctx, pr.ID, "planned"); err != nil {
			p.t.Fatal(err)
		}
		if err := create("planned", owner, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("reused plan %v", err)
		}
	})
}

// TestParityEnvPlaceholderRules: make secret keeps needs_value, replace
// never empties an existing secret, duplicate names are invalid, and two
// concurrent confirms of one plan give exactly one set.
func TestParityEnvPlaceholderRules(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("pr")
		pr := p.project(p.org(owner.ID), owner.ID, "envr")
		box, err := seal.New(bytes.Repeat([]byte{4}, 32))
		if err != nil {
			p.t.Fatal(err)
		}
		sealer := func(set string) store.EnvSealer {
			return func(key, plain string) (store.EnvSecret, error) {
				nonce, ct, err := box.Seal(seal.EnvAAD(pr.ID.String(), set, key), []byte(plain))
				return store.EnvSecret{Key: key, Plain: plain, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}, err
			}
		}
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "ph", []store.EnvKeyCreate{{Key: "PW"}}, nil, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		set, changed, err := p.st.MakeEnvKeysSecret(p.ctx, pr.ID, "ph", []string{"PW"}, sealer("ph"), owner.ID, p.at)
		if err != nil || len(changed) != 1 || !set.Keys[0].IsSecret || !set.Keys[0].NeedsValue {
			p.t.Fatalf("make secret %+v %v", set.Keys, err)
		}
		// Replace with "" over the existing secret: refused, nothing written.
		empty, _ := sealer("ph")("PW", "")
		if _, err := p.st.PutEnvSet(p.ctx, pr.ID, "ph", []store.EnvSecret{empty}, owner.ID, p.at, true); !errors.Is(err, store.ErrSecretEmpty) {
			p.t.Fatalf("replace emptied a secret: %v", err)
		}
		mats, _ := p.st.ListEnvMaterial(p.ctx, pr.ID, "ph")
		if len(mats) != 1 || !mats[0].NeedsValue {
			p.t.Fatalf("placeholder changed %+v", mats)
		}
		// A typed value clears it.
		filled, _ := sealer("ph")("PW", "typed-secret-1")
		if _, err := p.st.PutEnvSet(p.ctx, pr.ID, "ph", []store.EnvSecret{filled}, owner.ID, p.at, true); err != nil {
			p.t.Fatal(err)
		}
		mats, _ = p.st.ListEnvMaterial(p.ctx, pr.ID, "ph")
		if mats[0].NeedsValue {
			p.t.Fatal("a typed value left needs_value")
		}
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "dup", []store.EnvKeyCreate{{Key: "A"}, {Key: "A"}}, nil, owner.ID, p.at); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("duplicate keys %v", err)
		}

		// Two concurrent confirms of one plan: exactly one success.
		_, hash, _ := secret.NewToken()
		plan := store.EnvSetConfirmation{
			UserID: owner.ID, ProjectID: pr.ID, SetName: "race", Body: json.RawMessage(`{"keys":[{"key":"A","is_secret":false}]}`),
			RequestHash: bytes.Repeat([]byte{2}, 32), IdempotencyKey: "race-1", CreatedAt: p.at, ExpiresAt: p.at.Add(store.ConfirmationTTL),
		}
		if err := p.st.PutEnvSetConfirmation(p.ctx, hash, plan); err != nil {
			p.t.Fatal(err)
		}
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "race", []store.EnvKeyCreate{{Key: "A"}}, hash, owner.ID, p.at)
				results <- err
			}()
		}
		ok := 0
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict):
			default:
				p.t.Fatalf("concurrent confirm %v", err)
			}
		}
		if ok != 1 {
			p.t.Fatalf("%d confirms succeeded", ok)
		}
	})
}
