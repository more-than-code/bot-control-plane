package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	changelog "github.com/more-than-code/bot-control-plane"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Notifications and read marks (api-v1 v1.37 items 1–3). Items are computed
// on each call and never stored; only read marks are (migration 0024).
const (
	maxNotifications      = 20
	maxReleaseNotices     = 5
	releaseNoticeWindow   = 90 * 24 * time.Hour
	maxNotificationMarks  = 50
	maxChangelogLookahead = 1000
)

// notification is one item of GET /v1/me/notifications.
type notification struct {
	Key    string    `json:"key"`
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Href   string    `json:"href"`
	At     time.Time `json:"at"`
	Read   bool      `json:"read"`
	// Level and State are on problem items only (api-v1 v1.41 item 3).
	Level string `json:"level,omitempty"`
	State string `json:"state,omitempty"`
}

// releaseEntries is the server's own CHANGELOG.md, newest first, unless a
// test set s.releases.
func (s *Server) releaseEntries() []changelog.Entry {
	if s.releases != nil {
		return s.releases()
	}
	return changelog.Entries(maxChangelogLookahead)
}

// releaseNotices are the releases dated within the last 90 days, at most 5.
// Days are calendar days in UTC: a release dated 90 days before today is in.
func (s *Server) releaseNotices(now time.Time) []notification {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var out []notification
	for _, e := range s.releaseEntries() {
		if len(out) == maxReleaseNotices {
			break
		}
		day, err := time.Parse("2006-01-02", e.Date)
		if err != nil || today.Sub(day) > releaseNoticeWindow {
			continue
		}
		detail := ""
		if len(e.Items) > 0 {
			detail = e.Items[0]
		}
		out = append(out, notification{
			Key:    "release:" + e.Version,
			Kind:   "release",
			Title:  "Deploybot v" + e.Version + " is out",
			Detail: detail,
			Href:   "/whats-new#v" + e.Version,
			At:     day.UTC(),
		})
	}
	return out
}

// hostUpdateNotices are the hosts with update_available (v1.34, v1.36) in
// orgs where the caller is owner or admin.
func (s *Server) hostUpdateNotices(ctx context.Context, userID uuid.UUID) ([]notification, error) {
	if s.LatestAgentVersion == "" {
		return nil, nil
	}
	orgs, err := s.Store.ListUserOrgs(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []notification
	for _, o := range orgs {
		if !store.OrgAdmin(o.Role) {
			continue
		}
		hosts, _, err := s.Store.ListOrgHosts(ctx, o.ID, 10000, nil)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			v := s.withUpdateHint(h, nil)
			if !v.UpdateAvailable {
				continue
			}
			latest := *v.LatestAgentVersion
			// A host that never connected has no last_seen_at: use its enrolment.
			at := h.EnrolledAt
			if h.LastSeenAt != nil {
				at = *h.LastSeenAt
			}
			out = append(out, notification{
				Key:    "host_update:" + h.ID.String() + ":" + latest,
				Kind:   "host_update",
				Title:  "Update available on " + h.Name,
				Detail: "bot-agent " + latest + " (this host: " + h.AgentVersion + ")",
				Href:   "/hosts/" + h.ID.String(),
				At:     at.UTC(),
			})
		}
	}
	return out, nil
}

// notifications is the caller's items without read flags: open problems
// first and never cut (api-v1 v1.41 item 3), then the rest newest first,
// filling up to 20 items.
func (s *Server) notifications(ctx context.Context, userID uuid.UUID) ([]notification, error) {
	hosts, err := s.hostUpdateNotices(ctx, userID)
	if err != nil {
		return nil, err
	}
	open, resolved, err := s.problemNotices(ctx, userID)
	if err != nil {
		return nil, err
	}
	rest := append(s.releaseNotices(s.now()), hosts...)
	rest = append(rest, resolved...)
	// Stable on equal at: releases dated the same day keep the CHANGELOG
	// order (newest first; key order would put 0.2.37 above 0.2.38), then
	// hosts in their list order.
	sort.SliceStable(rest, func(i, j int) bool {
		return rest[i].At.After(rest[j].At)
	})
	if room := max(0, maxNotifications-len(open)); len(rest) > room {
		rest = rest[:room]
	}
	return append(open, rest...), nil
}

func notificationKeys(items []notification) []string {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Key
	}
	return keys
}

// handleNotifications is GET /v1/me/notifications. Console session only:
// the route refuses api_delegated, and requireConsole any other kind.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, "", err)
		return
	}
	items, err := s.notifications(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	reads, err := s.Store.NotificationReads(r.Context(), p.User.ID, notificationKeys(items))
	if err != nil {
		s.fail(w, err)
		return
	}
	unread := 0
	for i := range items {
		// A resolved problem is never unread (api-v1 v1.41 item 3).
		items[i].Read = reads[items[i].Key] || items[i].State == "resolved"
		if !items[i].Read {
			unread++
		}
	}
	if items == nil {
		items = []notification{}
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items, "unread": unread})
}

// handleNotificationsRead is POST /v1/me/notifications/read: keys or all,
// 200 with a null payload, not audited. The current items are computed for
// both forms: all marks them, and old marks for them are never pruned.
func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, "", err)
		return
	}
	var body struct {
		Keys []string `json:"keys"`
		All  bool     `json:"all"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if (body.Keys != nil) == body.All {
		writeCode(w, "bad_request", "send keys or all")
		return
	}
	keys := body.Keys
	if !body.All {
		if len(keys) > maxNotificationMarks || len(keys) == 0 {
			writeCode(w, "bad_request", "invalid keys")
			return
		}
		for _, k := range keys {
			if !store.ValidNotificationKey(k) {
				writeCode(w, "bad_request", "invalid keys")
				return
			}
		}
	}
	items, err := s.notifications(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	current := notificationKeys(items)
	if body.All {
		keys = current
	}
	if len(keys) > 0 {
		if err := s.Store.MarkNotificationsRead(r.Context(), p.User.ID, keys, current, s.now()); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeOK(w, http.StatusOK, nil)
}
