package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestParityMakeSecretEmptyValue is spec-release-v0239 F6 in both stores:
// MakeEnvKeysSecret refuses a plain key whose value is empty (and that is not
// a needs_value placeholder) with EmptySecretKeyError, which matches
// ErrSecretEmpty, and changes nothing; a placeholder may still be made secret.
func TestParityMakeSecretEmptyValue(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("me")
		pr := p.project(p.org(owner.ID), owner.ID, "envempty")
		box, err := seal.New(bytes.Repeat([]byte{6}, 32))
		if err != nil {
			p.t.Fatal(err)
		}
		sealer := func(key, plain string) (store.EnvSecret, error) {
			nonce, ct, err := box.Seal(seal.EnvAAD(pr.ID.String(), "s", key), []byte(plain))
			return store.EnvSecret{Key: key, Plain: plain, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}, err
		}
		full, empty := "value-0123456789", ""
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "s", []store.EnvKeyCreate{
			{Key: "FULL", Value: &full}, {Key: "EMPTY", Value: &empty}, {Key: "PH"},
		}, nil, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		_, _, err = p.st.MakeEnvKeysSecret(p.ctx, pr.ID, "s", []string{"FULL", "EMPTY"}, sealer, owner.ID, p.at)
		var refused *store.EmptySecretKeyError
		if !errors.As(err, &refused) || refused.Key != "EMPTY" || !errors.Is(err, store.ErrSecretEmpty) {
			p.t.Fatalf("empty value made secret: %v", err)
		}
		mats, err := p.st.ListEnvMaterial(p.ctx, pr.ID, "s")
		if err != nil {
			p.t.Fatal(err)
		}
		for _, m := range mats {
			if m.IsSecret {
				p.t.Fatalf("a refused call changed %s", m.Key)
			}
		}
		set, changed, err := p.st.MakeEnvKeysSecret(p.ctx, pr.ID, "s", []string{"FULL", "PH"}, sealer, owner.ID, p.at)
		if err != nil || len(changed) != 2 {
			p.t.Fatalf("make secret %v %v", changed, err)
		}
		for _, k := range set.Keys {
			if k.Key == "PH" && (!k.IsSecret || !k.NeedsValue) {
				p.t.Fatalf("placeholder %+v", k)
			}
		}
	})
}

// TestParityEnvSetChangeMakeSecretEmpty is spec-release-v0239 F6 on the v1.33
// update path (ApplyEnvSetChange) in both stores: make_secret on an empty
// plain key is EmptySecretKeyError, nothing changes and the plan stays
// unused; a needs_value placeholder still becomes a secret placeholder.
func TestParityEnvSetChangeMakeSecretEmpty(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("mc")
		pr := p.project(p.org(owner.ID), owner.ID, "envchg")
		box, err := seal.New(bytes.Repeat([]byte{8}, 32))
		if err != nil {
			p.t.Fatal(err)
		}
		sealer := func(key, plain string) (store.EnvSecret, error) {
			nonce, ct, err := box.Seal(seal.EnvAAD(pr.ID.String(), "s", key), []byte(plain))
			return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}, err
		}
		full, empty := "value-0123456789", ""
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "s", []store.EnvKeyCreate{
			{Key: "FULL", Value: &full}, {Key: "EMPTY", Value: &empty}, {Key: "PH"},
		}, nil, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		_, hash, _ := secret.NewToken()
		if err := p.st.PutEnvSetConfirmation(p.ctx, hash, store.EnvSetConfirmation{
			UserID: owner.ID, ProjectID: pr.ID, SetName: "s", Body: json.RawMessage(`{"action":"update"}`),
			RequestHash: bytes.Repeat([]byte{1}, 32), IdempotencyKey: "k", CreatedAt: p.at, ExpiresAt: p.at.Add(store.ConfirmationTTL),
		}); err != nil {
			p.t.Fatal(err)
		}
		state := func() string {
			p.t.Helper()
			mats, err := p.st.ListEnvMaterial(p.ctx, pr.ID, "s")
			if err != nil {
				p.t.Fatal(err)
			}
			for _, m := range mats {
				if m.Key != "PH" && m.IsSecret {
					p.t.Fatalf("%s became secret", m.Key)
				}
			}
			return store.EnvStateHash(mats)
		}
		_, err = p.st.ApplyEnvSetChange(p.ctx, pr.ID, "s", store.EnvSetChange{MakeSecret: []string{"FULL", "EMPTY"}, StateHash: state()}, hash, sealer, owner.ID, p.at)
		var refused *store.EmptySecretKeyError
		if !errors.As(err, &refused) || refused.Key != "EMPTY" || !errors.Is(err, store.ErrSecretEmpty) {
			p.t.Fatalf("empty value made secret: %v", err)
		}
		if c, err := p.st.GetEnvSetConfirmation(p.ctx, hash); err != nil || c.UsedAt != nil {
			p.t.Fatalf("refused change used the plan %+v %v", c, err)
		}
		set, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "s", store.EnvSetChange{MakeSecret: []string{"FULL", "PH"}, StateHash: state()}, hash, sealer, owner.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		for _, k := range set.Keys {
			if (k.Key == "PH" && (!k.IsSecret || !k.NeedsValue)) || (k.Key == "FULL" && !k.IsSecret) || (k.Key == "EMPTY" && k.IsSecret) {
				p.t.Fatalf("after change %+v", set.Keys)
			}
		}
	})
}
