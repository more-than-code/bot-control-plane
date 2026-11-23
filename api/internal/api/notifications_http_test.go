package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	changelog "github.com/more-than-code/bot-control-plane"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// notificationsWire is GET /v1/me/notifications (api-v1 v1.37 item 1).
type notificationsWire struct {
	Items []struct {
		Key    string    `json:"key"`
		Kind   string    `json:"kind"`
		Title  string    `json:"title"`
		Detail string    `json:"detail"`
		Href   string    `json:"href"`
		At     time.Time `json:"at"`
		Read   bool      `json:"read"`
	} `json:"items"`
	Unread int `json:"unread"`
}

func getNotifications(t *testing.T, w *world, token string) notificationsWire {
	t.Helper()
	status, env, raw := mustCall(t, w, token, http.MethodGet, "/v1/me/notifications", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var out struct {
		Payload notificationsWire `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Payload.Items == nil {
		t.Fatalf("items must be an array: %s", raw)
	}
	return out.Payload
}

func wireKeys(n notificationsWire) []string {
	out := make([]string, len(n.Items))
	for i, it := range n.Items {
		out[i] = it.Key
	}
	return out
}

// markRead posts body to /v1/me/notifications/read and returns the status,
// envelope and raw body.
func markRead(t *testing.T, w *world, token, body string) (int, envelope, string) {
	t.Helper()
	status, env, raw, err := callRaw(w, token, http.MethodPost, "/v1/me/notifications/read", body)
	if err != nil {
		t.Fatalf("%v: %q", err, raw)
	}
	return status, env, raw
}

func releasesAt(now time.Time, ages ...int) []changelog.Entry {
	out := make([]changelog.Entry, len(ages))
	for i, days := range ages {
		out[i] = changelog.Entry{
			Version: fmt.Sprintf("0.9.%d", len(ages)-i),
			Date:    now.AddDate(0, 0, -days).Format("2006-01-02"),
			Items:   []string{fmt.Sprintf("First line of 0.9.%d.", len(ages)-i), "Second line."},
		}
	}
	return out
}

func touchAt(t *testing.T, w *world, host store.Host, at time.Time, version string) {
	t.Helper()
	if err := w.mem.TouchHost(context.Background(), host.ID, at, version, "", ""); err != nil {
		t.Fatal(err)
	}
}

// notificationWorld: an org owner, an org admin, an org member, a project
// member who is not in the org, and four hosts in the owner's org.
type notificationScene struct {
	w                               *world
	owner, admin, member, projOnly  store.User
	project                         store.Project
	old, current, revoked, neverSaw store.Host
}

func newNotificationScene(t *testing.T) notificationScene {
	t.Helper()
	w := newWorld(t)
	ctx := context.Background()
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("admin@ex.com", "Admin")
	member := w.user("member@ex.com", "Member")
	projOnly := w.user("proj@ex.com", "Proj Only")
	org := w.mem.PersonalOrg(owner.ID)
	if _, err := w.mem.AddOrgMember(ctx, org, admin.ID, store.OrgRoleAdmin, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddOrgMember(ctx, org, member.ID, "member", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	p, err := w.mem.CreateProject(ctx, org, owner.ID, "Proj", "proj", w.now)
	if err != nil {
		t.Fatal(err)
	}
	// Project roles do not give host items, deploy access or not.
	for _, u := range []store.User{member, projOnly} {
		if _, err := w.mem.AddMember(ctx, p.ID, u.ID, "admin", owner.ID, w.now); err != nil {
			t.Fatal(err)
		}
	}
	old := enrollAt(t, w, p, owner.ID, "old", "0.2.9")
	touchAt(t, w, old, w.now.Add(-time.Hour), "")
	current := enrollAt(t, w, p, owner.ID, "current", "0.3.0")
	touchAt(t, w, current, w.now.Add(-time.Minute), "")
	revoked := enrollAt(t, w, p, owner.ID, "revoked", "0.2.1")
	touchAt(t, w, revoked, w.now.Add(-2*time.Hour), "")
	if err := w.mem.RevokeHost(ctx, revoked.ID, w.now); err != nil {
		t.Fatal(err)
	}
	neverSaw := enrollAt(t, w, p, owner.ID, "never", "0.2.8")
	w.srv.LatestAgentVersion = "0.3.0"
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 1) }
	return notificationScene{w: w, owner: owner, admin: admin, member: member, projOnly: projOnly, project: p,
		old: old, current: current, revoked: revoked, neverSaw: neverSaw}
}

// TestNotificationsReleases is the release items: the last 90 days only, at
// most 5, newest first, with the v1.37 title, detail, href and at.
func TestNotificationsReleases(t *testing.T) {
	w := newWorld(t)
	u := w.user("r@ex.com", "R")
	tok := w.token(u.ID)

	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0, 3, 89, 90, 91, 200) }
	got := getNotifications(t, w, tok)
	if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.6,release:0.9.5,release:0.9.4,release:0.9.3" {
		t.Fatalf("90 days %s", keys)
	}
	first := got.Items[0]
	day := time.Date(w.now.Year(), w.now.Month(), w.now.Day(), 0, 0, 0, 0, time.UTC)
	if first.Kind != "release" || first.Title != "Deploybot v0.9.6 is out" || first.Detail != "First line of 0.9.6." ||
		first.Href != "/whats-new#v0.9.6" || !first.At.Equal(day) || first.Read || got.Unread != 4 {
		t.Fatalf("release item %+v unread %d", first, got.Unread)
	}

	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 1, 2, 3, 4, 5, 6, 7) }
	got = getNotifications(t, w, tok)
	if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.7,release:0.9.6,release:0.9.5,release:0.9.4,release:0.9.3" {
		t.Fatalf("at most 5: %s", keys)
	}

	// Two releases on one day keep the CHANGELOG order (newest first), not key order.
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 1, 1, 2) }
	got = getNotifications(t, w, tok)
	if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.3,release:0.9.2,release:0.9.1" {
		t.Fatalf("same day: %s", keys)
	}

	// The embedded CHANGELOG.md is the default source.
	w.srv.releases = nil
	latest, ok := changelog.Latest()
	if !ok {
		t.Fatal("changelog")
	}
	date, _ := time.Parse("2006-01-02", latest.Date)
	w.srv.Now = func() time.Time { return date.Add(12 * time.Hour) }
	got = getNotifications(t, w, tok)
	if len(got.Items) == 0 || got.Items[0].Key != "release:"+latest.Version || got.Items[0].Detail != latest.Items[0] {
		t.Fatalf("embedded changelog %+v", got.Items)
	}
}

// TestNotificationsHostUpdates: host items only for org owners and admins,
// never for revoked or current hosts, dated when the API first saw the host
// behind the latest version and sorted with the releases by at.
func TestNotificationsHostUpdates(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	oldKey := "host_update:" + sc.old.ID.String() + ":0.3.0"
	neverKey := "host_update:" + sc.neverSaw.ID.String() + ":0.3.0"
	// The first list is two days ago: both hosts are noticed then, a day
	// before the release.
	noticed := w.now.Add(-48 * time.Hour)
	w.srv.Now = func() time.Time { return noticed }
	getNotifications(t, w, w.token(sc.owner.ID))
	w.srv.Now = func() time.Time { return w.now }
	for _, u := range []store.User{sc.owner, sc.admin} {
		got := getNotifications(t, w, w.token(u.ID))
		// Equal at keeps the host list order (by name).
		if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.1,"+neverKey+","+oldKey {
			t.Fatalf("%s: %s", u.Email, keys)
		}
		host := got.Items[2]
		if host.Kind != "host_update" || host.Title != "Update available on old" || host.Detail != "bot-agent 0.3.0 (this host: 0.2.9)" ||
			host.Href != "/hosts/"+sc.old.ID.String() || !host.At.Equal(noticed) || host.Read || got.Unread != 3 {
			t.Fatalf("host item %+v unread %d", host, got.Unread)
		}
	}
	// A check-in does not move a notice back to the top.
	touchAt(t, w, sc.old, w.now, "")
	touchAt(t, w, sc.neverSaw, w.now, "")
	if keys := strings.Join(wireKeys(getNotifications(t, w, w.token(sc.owner.ID))), ","); keys != "release:0.9.1,"+neverKey+","+oldKey {
		t.Fatalf("after check-in %s", keys)
	}
	for _, u := range []store.User{sc.member, sc.projOnly} {
		got := getNotifications(t, w, w.token(u.ID))
		if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.1" {
			t.Fatalf("%s gets host items: %s", u.Email, keys)
		}
	}
	// No latest version configured: no host items.
	w.srv.LatestAgentVersion = ""
	if got := getNotifications(t, w, w.token(sc.owner.ID)); len(got.Items) != 1 {
		t.Fatalf("no latest %v", wireKeys(got))
	}
	// An upgraded host drops out; a newer latest changes the key and is
	// noticed now, above the day-old release.
	w.srv.LatestAgentVersion = "0.3.1"
	touchAt(t, w, sc.old, w.now, "0.3.1")
	got := getNotifications(t, w, w.token(sc.owner.ID))
	want := []string{"host_update:" + sc.current.ID.String() + ":0.3.1", neverKey[:len(neverKey)-len("0.3.0")] + "0.3.1", "release:0.9.1"}
	if keys := strings.Join(wireKeys(got), ","); keys != strings.Join(want, ",") || !got.Items[0].At.Equal(w.now) {
		t.Fatalf("after upgrade %s", keys)
	}
}

// TestNotificationsCap: at most 20 items, newest first.
func TestNotificationsCap(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	var bulk18 string
	for i := 0; i < 25; i++ {
		h := enrollAt(t, w, sc.project, sc.owner.ID, fmt.Sprintf("bulk%02d", i), "0.2.0")
		if _, err := w.mem.NoticeHostUpdates(context.Background(), []uuid.UUID{h.ID}, "0.3.0", w.now.Add(-time.Duration(i+10)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if i == 18 {
			bulk18 = "host_update:" + h.ID.String() + ":0.3.0"
		}
	}
	got := getNotifications(t, w, w.token(sc.owner.ID))
	if len(got.Items) != 20 || got.Unread != 20 {
		t.Fatalf("cap %d unread %d", len(got.Items), got.Unread)
	}
	for i := 1; i < len(got.Items); i++ {
		if got.Items[i].At.After(got.Items[i-1].At) {
			t.Fatalf("not newest first at %d: %v", i, wireKeys(got))
		}
	}
	// never and old (both noticed by this list, at w.now), then
	// bulk00..bulk17 (noticed 10 to 27 minutes ago); the other bulk hosts
	// and the release fall off.
	if got.Items[0].Title != "Update available on never" || got.Items[1].Title != "Update available on old" ||
		got.Items[19].Title != "Update available on bulk17" {
		t.Fatalf("last kept %s", got.Items[19].Title)
	}

	// An old mark on a returned item survives the all write below.
	first := got.Items[0].Key
	w.srv.Now = func() time.Time { return w.now.Add(-100 * 24 * time.Hour) }
	status, env, _ := markRead(t, w, w.token(sc.owner.ID), `{"keys":["`+first+`"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	w.srv.Now = func() time.Time { return w.now }

	// all marks exactly the 20 returned keys; the 21st item (bulk18) stays
	// unread.
	status, env, _ = markRead(t, w, w.token(sc.owner.ID), `{"all":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	keys := append(wireKeys(got), bulk18)
	reads, err := w.mem.NotificationReads(context.Background(), sc.owner.ID, keys)
	if err != nil || len(reads) != 20 || reads[bulk18] || !reads[first] {
		t.Fatalf("all marked %d bulk18=%v first=%v %v", len(reads), reads[bulk18], reads[first], err)
	}
	if after := getNotifications(t, w, w.token(sc.owner.ID)); after.Unread != 0 || len(after.Items) != 20 {
		t.Fatalf("after all unread %d items %d", after.Unread, len(after.Items))
	}
}

// TestNotificationsMarkRead covers POST /v1/me/notifications/read.
func TestNotificationsMarkRead(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	tok := w.token(sc.owner.ID)
	oldKey := "host_update:" + sc.old.ID.String() + ":0.3.0"

	status, _, raw := markRead(t, w, tok, `{"keys":["release:0.9.1","`+oldKey+`","unknown:key"]}`)
	if status != http.StatusOK || strings.TrimSpace(raw) != `{"code":"ok","msg":"ok","payload":null}` {
		t.Fatalf("keys %d %q", status, raw)
	}
	got := getNotifications(t, w, tok)
	if got.Unread != 1 {
		t.Fatalf("unread %d", got.Unread)
	}
	for _, it := range got.Items {
		if it.Read != (it.Key == "release:0.9.1" || it.Key == oldKey) {
			t.Fatalf("read flag %+v", it)
		}
	}
	// Marking again is fine; marks are per user.
	if status, _, _ := markRead(t, w, tok, `{"keys":["release:0.9.1","release:0.9.1"]}`); status != http.StatusOK {
		t.Fatalf("again %d", status)
	}
	adminTok := w.token(sc.admin.ID)
	if got := getNotifications(t, w, adminTok); got.Unread != 3 {
		t.Fatalf("admin unread %d", got.Unread)
	}
	// all marks every item the GET returns now.
	if status, _, _ := markRead(t, w, adminTok, `{"all":true}`); status != http.StatusOK {
		t.Fatalf("all %d", status)
	}
	if got := getNotifications(t, w, adminTok); got.Unread != 0 || len(got.Items) != 3 {
		t.Fatalf("after all %d %d", got.Unread, len(got.Items))
	}
	// A new release after all is unread.
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0, 1) }
	got = getNotifications(t, w, adminTok)
	if got.Unread != 1 {
		t.Fatalf("new release %+v", got.Items)
	}
	for _, it := range got.Items {
		if it.Read != (it.Key != "release:0.9.2") {
			t.Fatalf("new release %+v", it)
		}
	}

	long := strings.Repeat("k", 201)
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf(`"k%d"`, i)
	}
	fifty := strings.Join(many[:50], ",")
	for _, tc := range []struct{ body, msg string }{
		{`{}`, "send keys or all"},
		{`{"all":false}`, "send keys or all"},
		{`{"keys":["a"],"all":true}`, "send keys or all"},
		{`{"keys":[]}`, "invalid keys"},
		{`{"keys":[""]}`, "invalid keys"},
		{`{"keys":["` + long + `"]}`, "invalid keys"},
		{`{"keys":[` + strings.Join(many, ",") + `]}`, "invalid keys"},
		{`{"keys":["a\u0000b"]}`, "invalid keys"},
		{`{"keys":["a\u001fb"]}`, "invalid keys"},
		{`{"keys":["a\u007fb"]}`, "invalid keys"},
		{`{"keys":["\n"]}`, "invalid keys"},
		{`{"keys":"a"}`, "invalid json"},
		{`{"keys":["a"],"x":1}`, "invalid json"},
	} {
		status, env, _ := markRead(t, w, tok, tc.body)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
	}
	if status, _, _ := markRead(t, w, tok, `{"keys":[`+fifty+`]}`); status != http.StatusOK {
		t.Fatalf("50 keys %d", status)
	}
	if status, _, _ := markRead(t, w, tok, `{"keys":["`+strings.Repeat("é", 200)+`"]}`); status != http.StatusOK {
		t.Fatalf("200 characters %d", status)
	}
}

// TestNotificationsDelegatedForbidden: both routes are console only.
func TestNotificationsDelegatedForbidden(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	del := tokenKind(t, w, sc.owner.ID, store.KindDelegated)
	status, env, _ := mustCall(t, w, del, http.MethodGet, "/v1/me/notifications", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = markRead(t, w, del, `{"all":true}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, w, "", http.MethodGet, "/v1/me/notifications", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("no token %d %s", status, env.Code)
	}
	// The refused POST stored nothing.
	reads, err := w.mem.NotificationReads(context.Background(), sc.owner.ID, []string{"release:0.9.1"})
	if err != nil || len(reads) != 0 {
		t.Fatalf("reads %v %v", reads, err)
	}
}

// TestMeProjectOrgName is api-v1 v1.37 item 4.
func TestMeProjectOrgName(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	ctx := context.Background()
	if _, err := w.mem.RenameOrg(ctx, sc.project.OrgID, "Acme"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []store.User{sc.owner, sc.member, sc.projOnly} {
		status, env, raw := mustCall(t, w, w.token(u.ID), http.MethodGet, "/v1/me", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		var out struct {
			Payload struct {
				Projects []map[string]any `json:"projects"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range out.Payload.Projects {
			if p["id"] != sc.project.ID.String() {
				if _, ok := p["org_name"].(string); !ok {
					t.Fatalf("%s: project without org_name %v", u.Email, p)
				}
				continue
			}
			found = true
			if p["org_name"] != "Acme" || p["org_id"] != sc.project.OrgID.String() || p["via"] != "member" {
				t.Fatalf("%s: %v", u.Email, p)
			}
		}
		if !found {
			t.Fatalf("%s: project missing in %s", u.Email, raw)
		}
	}
	// Other project reads do not gain the field (v1.37 names /v1/me only).
	status, env, raw := mustCall(t, w, w.token(sc.owner.ID), http.MethodGet, "/v1/projects/"+sc.project.ID.String(), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, "org_name") {
		t.Fatalf("project read %s", raw)
	}
	status, env, raw = mustCall(t, w, w.token(sc.owner.ID), http.MethodGet, "/v1/projects", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, "org_name") {
		t.Fatalf("project list %s", raw)
	}
}

// TestNotificationsPruneKeepsCurrent: a write prunes the caller's marks
// older than 90 days, except those whose key is still a current item.
func TestNotificationsPruneKeepsCurrent(t *testing.T) {
	sc := newNotificationScene(t)
	w := sc.w
	tok := w.token(sc.owner.ID)
	oldKey := "host_update:" + sc.old.ID.String() + ":0.3.0"
	then := w.now.Add(-100 * 24 * time.Hour)
	w.srv.Now = func() time.Time { return then }
	status, env, _ := markRead(t, w, tok, `{"keys":["`+oldKey+`","gone:1"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	w.srv.Now = func() time.Time { return w.now }
	status, env, _ = markRead(t, w, tok, `{"keys":["release:0.9.1"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	reads, err := w.mem.NotificationReads(context.Background(), sc.owner.ID, []string{oldKey, "gone:1", "release:0.9.1"})
	if err != nil || !reads[oldKey] || reads["gone:1"] || !reads["release:0.9.1"] {
		t.Fatalf("reads %v %v", reads, err)
	}
	for _, it := range getNotifications(t, w, tok).Items {
		if it.Key == oldKey && !it.Read {
			t.Fatal("a long-standing host item turned unread")
		}
	}
}
