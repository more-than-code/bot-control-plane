package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	changelog "github.com/more-than-code/bot-control-plane"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.57 (spec-release-first-served): a release this server has
// served is dated by when it first served it, with date_only false; a
// release with no record keeps its CHANGELOG date at 00:00 UTC with
// date_only true; other kinds carry no date_only.
func TestNotificationsReleaseFirstServed(t *testing.T) {
	w := newWorld(t)
	u := w.user("rs@ex.com", "RS")
	tok := w.token(u.ID)
	ctx := context.Background()

	// 0.9.2 and 0.9.1 are both dated today (releasesAt counts down).
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0, 0) }
	served := w.now.Add(-30 * time.Minute)
	if err := w.mem.RecordReleaseServed(ctx, "0.9.2", served); err != nil {
		t.Fatal(err)
	}
	got := getNotifications(t, w, tok)
	if keys := strings.Join(wireKeys(got), ","); keys != "release:0.9.2,release:0.9.1" {
		t.Fatalf("order %s", keys)
	}
	day := time.Date(w.now.Year(), w.now.Month(), w.now.Day(), 0, 0, 0, 0, time.UTC)
	recorded, dated := got.Items[0], got.Items[1]
	if !recorded.At.Equal(served) || recorded.DateOnly == nil || *recorded.DateOnly {
		t.Fatalf("recorded %+v", recorded)
	}
	if !dated.At.Equal(day) || dated.DateOnly == nil || !*dated.DateOnly {
		t.Fatalf("dated %+v", dated)
	}

	// A later record never moves the first time.
	if err := w.mem.RecordReleaseServed(ctx, "0.9.2", w.now); err != nil {
		t.Fatal(err)
	}
	if got := getNotifications(t, w, tok); !got.Items[0].At.Equal(served) {
		t.Fatalf("moved %+v", got.Items[0])
	}
}

// A recorded release sorts by its real time among other items, not as
// midnight: it lands above a host update noticed before it was served.
func TestNotificationsReleaseFirstServedOrder(t *testing.T) {
	s := newNotificationScene(t)
	w := s.w
	ctx := context.Background()
	tok := w.token(s.owner.ID)
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0) }
	// The host updates are noticed now (first list); the release was served after.
	getNotifications(t, w, tok)
	w.srv.Now = func() time.Time { return w.now.Add(time.Hour) }
	if err := w.mem.RecordReleaseServed(ctx, "0.9.1", w.now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := getNotifications(t, w, tok)
	if got.Items[0].Key != "release:0.9.1" {
		t.Fatalf("first %s", strings.Join(wireKeys(got), ","))
	}
	for _, it := range got.Items[1:] {
		if it.DateOnly != nil {
			t.Fatalf("%s carries date_only", it.Key)
		}
	}
}

// failingReleases is the memstore with a ReleasesServed that fails, as an
// API on a database without migration 0038 (or without its grant).
type failingReleases struct {
	store.Store
	err error
}

func (f failingReleases) ReleasesServed(context.Context, []string) (map[string]time.Time, error) {
	return nil, f.err
}

// A failed read still answers: every release is dated by its CHANGELOG date
// with date_only true, and the warning names the cause but never a URL.
func TestNotificationsReleaseFirstServedReadFails(t *testing.T) {
	var buf bytes.Buffer
	w := newWorldLog(t, slog.New(slog.NewTextHandler(&buf, nil)))
	u := w.user("rf@ex.com", "RF")
	tok := w.token(u.ID)
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0) }
	if err := w.mem.RecordReleaseServed(context.Background(), "0.9.1", w.now); err != nil {
		t.Fatal(err)
	}
	day := time.Date(w.now.Year(), w.now.Month(), w.now.Day(), 0, 0, 0, 0, time.UTC)
	for _, cause := range []string{`relation "app.release_first_served" does not exist`, "dial postgres://deploybot_api:secret@db/deploybot"} {
		buf.Reset()
		w.srv.Store = failingReleases{Store: w.mem, err: errors.New(cause)}
		got := getNotifications(t, w, tok)
		it := got.Items[0]
		if !it.At.Equal(day) || it.DateOnly == nil || !*it.DateOnly {
			t.Fatalf("%s: %+v", cause, it)
		}
		out := buf.String()
		if !strings.Contains(out, "release first-served times unavailable") || strings.Contains(out, "secret") {
			t.Fatalf("log %q", out)
		}
	}
}
