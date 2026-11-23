package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host alert settings and mounts (api-v1 v1.49 items 3 and 4).

const (
	// maxDiskPath is the longest disk mount the metrics query accepts.
	maxDiskPath = 1024
	// hostDisksSince is how far back GET /v1/hosts/{id}/disks looks.
	hostDisksSince = 24 * time.Hour
)

// alertSettingsView is the contract body of the alert-settings routes.
type alertSettingsView struct {
	AlertsEnabled          bool `json:"alerts_enabled"`
	MemMinAvailablePercent *int `json:"mem_min_available_percent"`
	DiskMaxUsedPercent     *int `json:"disk_max_used_percent"`
	CPUMaxBusyPercent      *int `json:"cpu_max_busy_percent"`
}

func alertView(s store.HostAlertSettings) alertSettingsView {
	return alertSettingsView{AlertsEnabled: s.AlertsEnabled, MemMinAvailablePercent: s.MemMinAvailablePercent,
		DiskMaxUsedPercent: s.DiskMaxUsedPercent, CPUMaxBusyPercent: s.CPUMaxBusyPercent}
}

// handleAlertSettingsGet is GET /v1/hosts/{id}/alert-settings: anyone who
// can see the host.
func (s *Server) handleAlertSettingsGet(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostView, nil, nil, ""); !ok {
		return
	}
	settings, err := s.Store.GetHostAlertSettings(r.Context(), host.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, alertView(settings))
}

// alertSettingsMsg is the bad_request text per field.
var alertSettingsMsg = map[string]string{
	"mem":  fmt.Sprintf("mem_min_available_percent must be %d–%d or null", store.MemMinAvailablePercentMin, store.MemMinAvailablePercentMax),
	"disk": fmt.Sprintf("disk_max_used_percent must be %d–%d or null", store.DiskMaxUsedPercentMin, store.DiskMaxUsedPercentMax),
	"cpu":  fmt.Sprintf("cpu_max_busy_percent must be %d–%d or null", store.CPUMaxBusyPercentMin, store.CPUMaxBusyPercentMax),
}

// alertSettingsProblem is the first out-of-range field's message, or "".
func alertSettingsProblem(in store.HostAlertSettings) string {
	bad := func(v *int, lo, hi int) bool { return v != nil && (*v < lo || *v > hi) }
	switch {
	case bad(in.MemMinAvailablePercent, store.MemMinAvailablePercentMin, store.MemMinAvailablePercentMax):
		return alertSettingsMsg["mem"]
	case bad(in.DiskMaxUsedPercent, store.DiskMaxUsedPercentMin, store.DiskMaxUsedPercentMax):
		return alertSettingsMsg["disk"]
	case bad(in.CPUMaxBusyPercent, store.CPUMaxBusyPercentMin, store.CPUMaxBusyPercentMax):
		return alertSettingsMsg["cpu"]
	}
	return ""
}

// handleAlertSettingsPut is PUT /v1/hosts/{id}/alert-settings: org owners
// and admins, console sessions and MCP-delegated tokens alike (the route is
// delegatedAllow). The MCP server keeps the user's confirmation step itself
// (set_alert_settings, then confirm) and sends this PUT only after it, so
// the API holds no plan state.
func (s *Server) handleAlertSettingsPut(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostAlertSettings); !ok {
		return
	}
	var body struct {
		AlertsEnabled          *bool `json:"alerts_enabled"`
		MemMinAvailablePercent *int  `json:"mem_min_available_percent"`
		DiskMaxUsedPercent     *int  `json:"disk_max_used_percent"`
		CPUMaxBusyPercent      *int  `json:"cpu_max_busy_percent"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.AlertsEnabled == nil {
		writeCode(w, "bad_request", "alerts_enabled is required")
		return
	}
	in := store.HostAlertSettings{AlertsEnabled: *body.AlertsEnabled, MemMinAvailablePercent: body.MemMinAvailablePercent,
		DiskMaxUsedPercent: body.DiskMaxUsedPercent, CPUMaxBusyPercent: body.CPUMaxBusyPercent}
	if msg := alertSettingsProblem(in); msg != "" {
		writeCode(w, "bad_request", msg)
		return
	}
	if host.RevokedAt != nil {
		writeCode(w, "conflict", "the host is revoked")
		return
	}
	current, err := s.Store.GetHostAlertSettings(r.Context(), host.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.storeAlertSettings(r, p, host, current, in); err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, alertView(in))
}

// storeAlertSettings writes the settings and audits the changed fields. An
// unchanged body writes and audits nothing.
func (s *Server) storeAlertSettings(r *http.Request, p principal, host store.Host, current, in store.HostAlertSettings) error {
	changed := alertSettingsChanges(current, in)
	if len(changed) == 0 {
		return nil
	}
	if err := s.Store.PutHostAlertSettings(r.Context(), host.ID, in, p.User.ID, s.now().UTC()); err != nil {
		return err
	}
	row := hostAuditRow(host, nil, audit.ActionHostAlertSettings)
	detail := map[string]any{"host": host.Name}
	for k, v := range changed {
		detail[k] = v
	}
	row.Detail = detail
	s.auditOK(r.Context(), p, row)
	return nil
}

// alertSettingsChanges is the changed fields with their new values (nil
// for back to the default).
func alertSettingsChanges(from, to store.HostAlertSettings) map[string]any {
	out := map[string]any{}
	if from.AlertsEnabled != to.AlertsEnabled {
		out["alerts_enabled"] = to.AlertsEnabled
	}
	pct := func(name string, a, b *int) {
		if (a == nil) != (b == nil) || (a != nil && *a != *b) {
			if b == nil {
				out[name] = nil
			} else {
				out[name] = *b
			}
		}
	}
	pct("mem_min_available_percent", from.MemMinAvailablePercent, to.MemMinAvailablePercent)
	pct("disk_max_used_percent", from.DiskMaxUsedPercent, to.DiskMaxUsedPercent)
	pct("cpu_max_busy_percent", from.CPUMaxBusyPercent, to.CPUMaxBusyPercent)
	return out
}

// handleHostDisks is GET /v1/hosts/{id}/disks: the mounts the host
// reported in the last 24 hours.
func (s *Server) handleHostDisks(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostView, nil, nil, ""); !ok {
		return
	}
	disks, err := s.Store.ListHostDisks(r.Context(), host.ID, s.now().Add(-hostDisksSince))
	if err != nil {
		s.fail(w, err)
		return
	}
	type diskView struct {
		Path   string `json:"path"`
		Fstype string `json:"fstype"`
	}
	items := make([]diskView, 0, len(disks))
	for _, d := range disks {
		items = append(items, diskView{Path: d.Path, Fstype: d.Fstype})
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}
