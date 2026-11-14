package api

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestEnvHashWithNetworks covers the combined path of api-v1 v1.25 and v1.27:
// a host whose hello advertises both shared_networks and env_hash, and an
// entry that joins a shared network and holds a ${KEY} reference. A console
// deploy and a delegated issue-then-confirm both carry spec.networks and an
// env_hash over the resolved env.
func TestEnvHashWithNetworks(t *testing.T) {
	programs := helloNets(`[{"name":"dd","subnet":"10.200.10.0/24","projects":["proj"]}]`)
	programs["env_hash"] = true
	w, sc, tok, log := refsWorld(t, programs)
	if sc.project.Slug != "proj" {
		// The network must be open to the scene's project.
		t.Fatalf("scene project slug %q", sc.project.Slug)
	}
	h, err := w.mem.GetHost(context.Background(), sc.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p := h.Programs; p == nil || !p.Enabled || !p.EnvHash || len(p.SharedNetworks) != 1 || p.SharedNetworks[0].Name != "dd" {
		t.Fatalf("stored programs %+v", h.Programs)
	}
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["dd"],"env_sets":["db"],"env":{"URL":"u:${POSTGRES_PASSWORD}"}}`)

	check := func(label string) {
		t.Helper()
		frame := lastSpec(t, log)
		nets, ok := networksOf(t, log.raw(log.count()-1))
		if list, _ := nets.([]any); !ok || len(list) != 1 || list[0] != "dd" {
			t.Fatalf("%s: spec.networks %v", label, nets)
		}
		envMap, _ := frame["env"].(map[string]any)
		if envMap["URL"] != "u:"+refPassword {
			t.Fatalf("%s: reference not resolved", label)
		}
		hash, _ := frame["env_hash"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
			t.Fatalf("%s: env_hash %q", label, hash)
		}
		resolved := map[string]string{}
		for k, v := range envMap {
			resolved[k], _ = v.(string)
		}
		if want, err := w.srv.Secrets.EnvHash(resolved); err != nil || want != hash {
			t.Fatalf("%s: env_hash is not the hash of the resolved env", label)
		}
	}

	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "n-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	check("console deploy")

	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "n-del", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("n-del"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before+1 {
		t.Fatal("confirm sent no task")
	}
	check("delegated confirm")
}
