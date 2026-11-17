package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestParityConfirmationPrune is spec-release-v0239 F4 in both stores: a new
// plan in catalog_write_confirmations, env_set_confirmations or
// edge_confirmations drops only rows that expired more than
// store.RequestKeep ago. A row that has only just expired stays, so its
// confirm still answers the expired conflict instead of not_found. A row
// expired exactly store.RequestKeep before the new plan stays too (the prune
// is strict).
func TestParityConfirmationPrune(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("cp")
		org := p.org(owner.ID)
		pr := p.project(org, owner.ID, "cprune")
		recent := p.at.Add(-time.Second)
		cutoff := p.at.Add(-store.RequestKeep)
		stale := cutoff.Add(-time.Second)
		token := func() []byte {
			p.t.Helper()
			_, hash, err := secret.NewToken()
			if err != nil {
				p.t.Fatal(err)
			}
			return hash
		}
		// check runs put for an expired row, a row at the cutoff, a stale
		// row and then a new plan, and reads each row back.
		check := func(table string, put func(hash []byte, key string, expires time.Time) error, get func(hash []byte) error) {
			p.t.Helper()
			recentHash, cutoffHash, staleHash, newHash := token(), token(), token(), token()
			if err := put(recentHash, "recent", recent); err != nil {
				p.t.Fatalf("%s recent: %v", table, err)
			}
			if err := put(cutoffHash, "cutoff", cutoff); err != nil {
				p.t.Fatalf("%s cutoff: %v", table, err)
			}
			if err := put(staleHash, "stale", stale); err != nil {
				p.t.Fatalf("%s stale: %v", table, err)
			}
			if err := put(newHash, "new", p.at.Add(store.ConfirmationTTL)); err != nil {
				p.t.Fatalf("%s new: %v", table, err)
			}
			if err := get(recentHash); err != nil {
				p.t.Fatalf("%s: a new plan dropped a recently expired row: %v", table, err)
			}
			if err := get(cutoffHash); err != nil {
				p.t.Fatalf("%s: a new plan dropped a row expired exactly RequestKeep ago: %v", table, err)
			}
			if err := get(staleHash); !errors.Is(err, store.ErrNotFound) {
				p.t.Fatalf("%s: a new plan kept a stale row: %v", table, err)
			}
			if err := get(newHash); err != nil {
				p.t.Fatalf("%s: new plan %v", table, err)
			}
		}

		check("catalog_write_confirmations", func(hash []byte, key string, expires time.Time) error {
			return p.st.PutCatalogWriteConfirmation(p.ctx, hash, store.CatalogWriteConfirmation{
				UserID: owner.ID, ProjectID: pr.ID, Action: "create", EntryName: "e-" + key,
				Body: json.RawMessage(`{"kind":"service","image":"nginx"}`), RequestHash: bytes.Repeat([]byte{1}, 32),
				IdempotencyKey: "cw-" + key, CreatedAt: p.at, ExpiresAt: expires,
			})
		}, func(hash []byte) error {
			_, err := p.st.GetCatalogWriteConfirmation(p.ctx, hash)
			return err
		})

		check("env_set_confirmations", func(hash []byte, key string, expires time.Time) error {
			return p.st.PutEnvSetConfirmation(p.ctx, hash, store.EnvSetConfirmation{
				UserID: owner.ID, ProjectID: pr.ID, SetName: "s-" + key,
				Body: json.RawMessage(`{"keys":[{"key":"A"}]}`), RequestHash: bytes.Repeat([]byte{2}, 32),
				IdempotencyKey: "es-" + key, CreatedAt: p.at, ExpiresAt: expires,
			})
		}, func(hash []byte) error {
			_, err := p.st.GetEnvSetConfirmation(p.ctx, hash)
			return err
		})

		spec, specHash, display := catalogSpec(p.t, "pnpm run dev")
		entry, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, store.CatalogWrite{Name: "pweb", Spec: spec, SpecHash: specHash, CommandDisplay: display, Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		check("edge_confirmations", func(hash []byte, key string, expires time.Time) error {
			return p.st.PutEdgeConfirmation(p.ctx, hash, store.EdgeConfirmation{
				UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &pr.ID, EntryID: &entry.ID,
				Body: json.RawMessage(`{}`), RequestHash: bytes.Repeat([]byte{3}, 32),
				IdempotencyKey: "ed-" + key, CreatedAt: p.at, ExpiresAt: expires,
			})
		}, func(hash []byte) error {
			_, err := p.st.GetEdgeConfirmation(p.ctx, hash)
			return err
		})
	})
}
