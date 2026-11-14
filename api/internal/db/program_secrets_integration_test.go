package db

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// assertProgramSecrets is the Postgres half of api-v1 v1.22 items 2 and 6,
// and the HostPrograms fields of v1.22 item 5 and v1.23 item 1.
func assertProgramSecrets(t *testing.T, d *DB, ctx context.Context, at time.Time) {
	t.Helper()
	owner := catalogUser(t, d, ctx, "progsec-owner", at)
	home := catalogProject(t, d, ctx, owner.ID, "ProgSec", "progsec", at)
	host := enrollHostDB(t, d, ctx, home.ID, owner.ID, "progsec-host", at)
	box, err := seal.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed := func(key, value string) store.EnvSecret {
		t.Helper()
		nonce, ct, err := box.Seal(seal.EnvAAD(home.ID.String(), "gh", key), []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}
	}
	if _, err := d.PutEnvSet(ctx, home.ID, "gh", []store.EnvSecret{sealed("GH_TOKEN", "db-prog-secret-1")}, owner.ID, at, true); err != nil {
		t.Fatal(err)
	}
	prog, err := catalog.BuildProgram(catalog.ProgramInput{
		Command: "gh api user", Dir: "/srv/app", RunAs: "joe", Env: map[string]string{"MODE": "ci"},
		SecretEnv: map[string]string{"GH_TOKEN": "gh.GH_TOKEN"}, Sets: catalog.SetKeys{"gh": {"GH_TOKEN"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := catalog.ProgramHash(prog, catalog.SetState{Keys: map[string][]string{"gh": {"GH_TOKEN"}}})
	_, err = d.CreateCatalogEntry(ctx, home.ID, store.CatalogWrite{
		Name: "gh", Spec: store.ProgramSpec{
			Dir: prog.Dir, Exec: prog.Exec, Args: prog.Args, RunAs: prog.RunAs, Env: prog.Env,
			EnvSets: prog.EnvSets, SecretEnv: prog.SecretEnv, Restart: prog.Restart,
		}, SpecHash: hash, CommandDisplay: prog.Display, Actor: owner.ID, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetCatalogEntry(ctx, home.ID, "gh")
	if err != nil || got.Spec.SecretEnv["GH_TOKEN"] != "gh.GH_TOKEN" || got.Spec.EnvSets == nil {
		t.Fatalf("stored spec %+v %v", got.Spec, err)
	}
	if err := d.DeleteEnvSet(ctx, home.ID, "gh"); err != store.ErrConflict {
		t.Fatalf("delete of a set a program uses: %v", err)
	}
	if _, err := d.PutEnvSet(ctx, home.ID, "gh", []store.EnvSecret{sealed("GH_TOKEN", "db-prog-secret-2")}, owner.ID, at.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	same, err := d.GetCatalogEntry(ctx, home.ID, "gh")
	if err != nil || same.SpecHash != hash {
		t.Fatalf("value-only change: hash %s %v", same.SpecHash, err)
	}
	if _, err := d.PutEnvSet(ctx, home.ID, "gh", []store.EnvSecret{sealed("GH_TOKEN", "db-prog-secret-2"), sealed("GH_HOST", "github.com")}, owner.ID, at.Add(2*time.Second), true); err != nil {
		t.Fatal(err)
	}
	changed, err := d.GetCatalogEntry(ctx, home.ID, "gh")
	want := catalog.ProgramHash(prog, catalog.SetState{Keys: map[string][]string{"gh": {"GH_TOKEN", "GH_HOST"}}})
	if err != nil || changed.SpecHash != want {
		t.Fatalf("key add: hash %s want %s %v", changed.SpecHash, want, err)
	}

	// An entry that uses a set only through env_sets (the env_sets ? $2 branch).
	cfgSealed := func(key, value string) store.EnvSecret {
		t.Helper()
		nonce, ct, err := box.Seal(seal.EnvAAD(home.ID.String(), "cfg", key), []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}
	}
	if _, err := d.PutEnvSet(ctx, home.ID, "cfg", []store.EnvSecret{cfgSealed("A_KEY", "a-value"), cfgSealed("B_KEY", "b-value")}, owner.ID, at, true); err != nil {
		t.Fatal(err)
	}
	setsOnly, err := catalog.BuildProgram(catalog.ProgramInput{
		Command: "gh api user", Dir: "/srv/app", RunAs: "joe",
		EnvSets: []string{"cfg"}, Sets: catalog.SetKeys{"cfg": {"A_KEY", "B_KEY"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	setsHash := catalog.ProgramHash(setsOnly, catalog.SetState{Keys: map[string][]string{"cfg": {"A_KEY", "B_KEY"}}})
	_, err = d.CreateCatalogEntry(ctx, home.ID, store.CatalogWrite{
		Name: "sets-only", Spec: store.ProgramSpec{
			Dir: setsOnly.Dir, Exec: setsOnly.Exec, Args: setsOnly.Args, RunAs: setsOnly.RunAs, Env: setsOnly.Env,
			EnvSets: setsOnly.EnvSets, SecretEnv: setsOnly.SecretEnv, Restart: setsOnly.Restart,
		}, SpecHash: setsHash, CommandDisplay: setsOnly.Display, Actor: owner.ID, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteEnvKey(ctx, home.ID, "cfg", "B_KEY", owner.ID, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := d.GetCatalogEntry(ctx, home.ID, "sets-only")
	wantSets := catalog.ProgramHash(setsOnly, catalog.SetState{Keys: map[string][]string{"cfg": {"A_KEY"}}})
	if err != nil || afterDelete.SpecHash != wantSets {
		t.Fatalf("key delete: hash %s want %s %v", afterDelete.SpecHash, wantSets, err)
	}
	if err := d.DeleteEnvSet(ctx, home.ID, "cfg"); err != store.ErrConflict {
		t.Fatalf("delete of a set used through env_sets: %v", err)
	}

	// api-v1 v1.22 amendment 1: a plain taking-part value is in the hash; a
	// plain value change re-hashes and voids approvals, a secret one does not.
	if _, err := d.PutEnvSet(ctx, home.ID, "pl", []store.EnvSecret{{Key: "HOST_URL", Plain: "https://a.example"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	s1 := sealedPl(t, box, home.ID.String(), "TOKEN_V", "pl-secret-1")
	if _, _, err := d.PutEnvKey(ctx, home.ID, "pl", store.EnvKeyPut{Key: "TOKEN_V", Plain: "x", Secret: boolRef(true),
		Nonce: s1.Nonce, Ciphertext: s1.Ciphertext, KeyVersion: seal.KeyVersion,
	}, owner.ID, at); err != nil {
		t.Fatal(err)
	}
	plProg, err := catalog.BuildProgram(catalog.ProgramInput{
		Command: "gh api user", Dir: "/srv/app", RunAs: "joe",
		EnvSets: []string{"pl"}, Sets: catalog.SetKeys{"pl": {"HOST_URL", "TOKEN_V"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plState := func(host string) catalog.SetState {
		return catalog.SetState{
			Keys:  map[string][]string{"pl": {"HOST_URL", "TOKEN_V"}},
			Plain: map[string]map[string]string{"pl": {"HOST_URL": host}},
		}
	}
	plHash := catalog.ProgramHash(plProg, plState("https://a.example"))
	_, err = d.CreateCatalogEntry(ctx, home.ID, store.CatalogWrite{
		Name: "plain-vals", Spec: store.ProgramSpec{
			Dir: plProg.Dir, Exec: plProg.Exec, Args: plProg.Args, RunAs: plProg.RunAs, Env: plProg.Env,
			EnvSets: plProg.EnvSets, SecretEnv: plProg.SecretEnv, Restart: plProg.Restart,
		}, SpecHash: plHash, CommandDisplay: plProg.Display, Actor: owner.ID, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	s2 := sealedPl(t, box, home.ID.String(), "TOKEN_V", "pl-secret-2")
	if _, _, err := d.PutEnvKey(ctx, home.ID, "pl", store.EnvKeyPut{Key: "TOKEN_V", Plain: "x", Nonce: s2.Nonce, Ciphertext: s2.Ciphertext, KeyVersion: seal.KeyVersion}, owner.ID, at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if e, err := d.GetCatalogEntry(ctx, home.ID, "plain-vals"); err != nil || e.SpecHash != plHash {
		t.Fatalf("secret value change: hash %s %v", e.SpecHash, err)
	}
	if _, _, err := d.PutEnvKey(ctx, home.ID, "pl", store.EnvKeyPut{Key: "HOST_URL", Plain: "https://b.example"}, owner.ID, at.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	wantPl := catalog.ProgramHash(plProg, plState("https://b.example"))
	if e, err := d.GetCatalogEntry(ctx, home.ID, "plain-vals"); err != nil || e.SpecHash != wantPl {
		t.Fatalf("plain value change: hash %s want %s %v", e.SpecHash, wantPl, err)
	}

	// An entry without sets is untouched by a rehash of any set.
	plain, err := catalog.BuildProgram(catalog.ProgramInput{Command: "pnpm run dev", Dir: "/srv/app", RunAs: "joe"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateCatalogEntry(ctx, home.ID, store.CatalogWrite{
		Name: "plain", Spec: store.ProgramSpec{Dir: plain.Dir, Exec: plain.Exec, Args: plain.Args, RunAs: plain.RunAs, Restart: plain.Restart},
		SpecHash: catalog.SpecHash(plain), CommandDisplay: plain.Display, Actor: owner.ID, At: at,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `
UPDATE app.catalog_entries SET spec = spec - 'env_sets' - 'secret_env' WHERE project_id = $1::uuid AND name = 'plain'
`, home.ID.String()); err != nil {
		t.Fatal(err)
	}
	old, err := d.GetCatalogEntry(ctx, home.ID, "plain")
	if err != nil || old.Spec.EnvSets == nil || old.Spec.SecretEnv == nil || old.SpecHash != catalog.SpecHash(plain) {
		t.Fatalf("pre-v1.22 spec %+v %v", old, err)
	}

	// Hello fields round-trip through the jsonb column; absent stays null.
	if err := d.SetHostPrograms(ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Secrets: true, Projects: []string{"progsec"}}); err != nil {
		t.Fatal(err)
	}
	h, err := d.GetHost(ctx, host.ID)
	if err != nil || h.Programs == nil || !h.Programs.Secrets || len(h.Programs.Projects) != 1 || h.Programs.Projects[0] != "progsec" {
		t.Fatalf("programs %+v %v", h.Programs, err)
	}
	if err := d.SetHostPrograms(ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}}); err != nil {
		t.Fatal(err)
	}
	h, err = d.GetHost(ctx, host.ID)
	if err != nil || h.Programs.Secrets || h.Programs.Projects != nil {
		t.Fatalf("older agent programs %+v %v", h.Programs, err)
	}
}

func sealedPl(t *testing.T, box *seal.Box, project, key, value string) store.EnvSecret {
	t.Helper()
	nonce, ct, err := box.Seal(seal.EnvAAD(project, "pl", key), []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct}
}
