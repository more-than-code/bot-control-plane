package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Notification read marks (api-v1 v1.37 item 3), as db/notifications.go.

func (m *Mem) MarkNotificationsRead(_ context.Context, userID uuid.UUID, keys, keep []string, at time.Time) error {
	for _, k := range keys {
		if !store.ValidNotificationKey(k) {
			return store.ErrInvalid
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(keys) > 0 {
		if _, ok := m.users[userID]; !ok {
			return store.ErrNotFound
		}
	}
	marks := m.notificationReads[userID]
	cutoff := at.Add(-store.NotificationReadRetention)
	current := map[string]bool{}
	for _, k := range keep {
		current[k] = true
	}
	for k, readAt := range marks {
		if readAt.Before(cutoff) && !current[k] {
			delete(marks, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if marks == nil {
		marks = map[string]time.Time{}
		m.notificationReads[userID] = marks
	}
	for _, k := range keys {
		marks[k] = at
	}
	if len(marks) <= store.MaxNotificationMarks {
		return nil
	}
	// Newest first (read_at, then key, both descending); delete past the cap.
	order := make([]string, 0, len(marks))
	for k := range marks {
		order = append(order, k)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := marks[order[i]], marks[order[j]]
		if !a.Equal(b) {
			return a.After(b)
		}
		return order[i] > order[j]
	})
	for _, k := range order[store.MaxNotificationMarks:] {
		if !current[k] {
			delete(marks, k)
		}
	}
	return nil
}

func (m *Mem) NotificationReads(_ context.Context, userID uuid.UUID, keys []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, k := range keys {
		if _, ok := m.notificationReads[userID][k]; ok {
			out[k] = true
		}
	}
	return out, nil
}

func (m *Mem) NoticeHostUpdates(_ context.Context, hostIDs []uuid.UUID, version string, at time.Time) (map[uuid.UUID]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uuid.UUID]time.Time{}
	for _, id := range hostIDs {
		if _, ok := m.hosts[id]; !ok {
			continue
		}
		if t, ok := m.hostUpdateNotices[id][version]; ok {
			out[id] = t
			continue
		}
		m.hostUpdateNotices[id] = map[string]time.Time{version: at}
		out[id] = at
	}
	return out, nil
}
