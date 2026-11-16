package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// updateHint is the api-v1 v1.34 item 2 pair as sent on a host. Latest is
// the raw JSON value ("" when the key is missing, "null" when null).
type updateHint struct {
	Latest          string
	UpdateAvailable *bool
}

func hintOf(t *testing.T, host map[string]json.RawMessage) (string, updateHint) {
	t.Helper()
	var name string
	if err := json.Unmarshal(host["name"], &name); err != nil {
		t.Fatal(err)
	}
	h := updateHint{Latest: string(host["latest_agent_version"])}
	if raw, ok := host["update_available"]; ok {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		h.UpdateAvailable = &b
	}
	return name, h
}

// enrollAt adds a host with the given agent version to the project's org,
// with deploy access for the project.
func enrollAt(t *testing.T, w *world, project store.Project, by uuid.UUID, name, version string) store.Host {
	t.Helper()
	ctx := context.Background()
	_, hash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.CreateJoinToken(ctx, project.OrgID, hash, nil, &store.TokenGrant{ProjectID: project.ID, Level: "deploy"}, by, w.now.Add(time.Hour), w.now); err != nil {
		t.Fatal(err)
	}
	_, credHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	host, err := w.mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{
		Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: version,
	}, uuid.New(), credHash, w.now)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func decodeHints(t *testing.T, payload any) map[string]updateHint {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]updateHint{}
	var list struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		for _, item := range list.Items {
			name, h := hintOf(t, item)
			out[name] = h
		}
		return out
	}
	var one map[string]json.RawMessage
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	name, h := hintOf(t, one)
	out[name] = h
	return out
}

// spec-update-awareness §5 item 6, on all three host reads.
func TestHostAgentUpdateHint(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("admin@ex.com", "Admin"), w.user("viewer@ex.com", "Viewer"), w.user("spare@ex.com", "Spare"), w.user("extra@ex.com", "Extra"))
	tok := w.token(owner.ID)
	ctx := context.Background()
	// The scene host (pi4) reports 0.2.9.
	if err := w.mem.TouchHost(ctx, sc.host.ID, w.now, "0.2.9", "", ""); err != nil {
		t.Fatal(err)
	}
	same := enrollAt(t, w, sc.project, owner.ID, "same", "0.2.10")
	enrollAt(t, w, sc.project, owner.ID, "empty", "")
	odd := enrollAt(t, w, sc.project, owner.ID, "odd", "0.2.3.2")
	newer := enrollAt(t, w, sc.project, owner.ID, "newer", "0.3.0")

	reads := map[string]string{
		"project": "/v1/projects/" + sc.project.ID.String() + "/hosts",
		"org":     "/v1/orgs/" + sc.project.OrgID.String() + "/hosts",
	}
	singles := map[string]uuid.UUID{"pi4": sc.host.ID, "same": same.ID, "odd": odd.ID, "newer": newer.ID}

	check := func(t *testing.T, latest string, want map[string]bool) {
		t.Helper()
		w.srv.LatestAgentVersion = latest
		got := map[string]map[string]updateHint{}
		for name, path := range reads {
			code, env := w.do(tok, http.MethodGet, path, "")
			if code != http.StatusOK {
				t.Fatalf("%s: %d %#v", name, code, env)
			}
			got[name] = decodeHints(t, env.Payload)
		}
		single := map[string]updateHint{}
		for name, id := range singles {
			code, env := w.do(tok, http.MethodGet, "/v1/hosts/"+id.String(), "")
			if code != http.StatusOK {
				t.Fatalf("host %s: %d %#v", name, code, env)
			}
			single[name] = decodeHints(t, env.Payload)[name]
		}
		got["single"] = single
		wantLatest := "null"
		if latest != "" {
			wantLatest = `"` + latest + `"`
		}
		for view, hosts := range got {
			for name, h := range hosts {
				wantUpdate, listed := want[name]
				if !listed {
					t.Fatalf("%s: unexpected host %q", view, name)
				}
				if h.UpdateAvailable == nil || *h.UpdateAvailable != wantUpdate {
					t.Errorf("latest %q %s %s: update_available %v, want %v", latest, view, name, h.UpdateAvailable, wantUpdate)
				}
				if h.Latest != wantLatest {
					t.Errorf("%s %s: latest_agent_version %q, want %s", view, name, h.Latest, wantLatest)
				}
			}
			if view != "single" && len(hosts) != len(want) {
				t.Errorf("%s: %d hosts, want %d", view, len(hosts), len(want))
			}
		}
	}

	t.Run("set", func(t *testing.T) {
		check(t, "0.2.10", map[string]bool{"pi4": true, "same": false, "empty": false, "odd": false, "newer": false})
	})
	t.Run("unset", func(t *testing.T) {
		check(t, "", map[string]bool{"pi4": false, "same": false, "empty": false, "odd": false, "newer": false})
	})
}
