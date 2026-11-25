package memstore

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host resource alerts (api-v1 v1.49). The aggregation matches
// db.HostResourceStats: samples in (now-15m, now], windows (now-w, now].

func cloneAlertSettings(s store.HostAlertSettings) store.HostAlertSettings {
	cp := func(v *int) *int {
		if v == nil {
			return nil
		}
		x := *v
		return &x
	}
	return store.HostAlertSettings{AlertsEnabled: s.AlertsEnabled, MemMinAvailablePercent: cp(s.MemMinAvailablePercent),
		DiskMaxUsedPercent: cp(s.DiskMaxUsedPercent), CPUMaxBusyPercent: cp(s.CPUMaxBusyPercent)}
}

func (m *Mem) GetHostAlertSettings(_ context.Context, hostID uuid.UUID) (store.HostAlertSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.alertSettings[hostID]; ok {
		return cloneAlertSettings(s), nil
	}
	return store.DefaultHostAlertSettings(), nil
}

func (m *Mem) PutHostAlertSettings(_ context.Context, hostID uuid.UUID, s store.HostAlertSettings, _ uuid.UUID, _ time.Time) error {
	if !store.ValidHostAlertSettings(s) {
		return store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[hostID]; !ok {
		return store.ErrNotFound
	}
	if m.alertSettings == nil {
		m.alertSettings = map[uuid.UUID]store.HostAlertSettings{}
	}
	m.alertSettings[hostID] = cloneAlertSettings(s)
	return nil
}

func (m *Mem) ListHostAlertSettings(_ context.Context) (map[uuid.UUID]store.HostAlertSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uuid.UUID]store.HostAlertSettings{}
	for id, s := range m.alertSettings {
		out[id] = cloneAlertSettings(s)
	}
	return out, nil
}

// resourceValue is the sample's value for measurement and, for mem, its
// total. ok is false when the sample does not count (db filter).
func resourceValue(measurement string, s store.Sample) (v, total float64, path string, ok bool) {
	switch measurement {
	case store.ResourceMem:
		if s.M != "mem" {
			return 0, 0, "", false
		}
		avail, ok1 := s.Ints["available"]
		tot, ok2 := s.Ints["total"]
		if !ok1 || !ok2 || tot <= 0 {
			return 0, 0, "", false
		}
		return float64(avail), float64(tot), "", true
	case store.ResourceDisk:
		if s.M != "disk" {
			return 0, 0, "", false
		}
		used, ok := s.Floats["used_percent"]
		return used, 0, s.Tags["path"], ok
	case store.ResourceCPU:
		if s.M != "cpu" || s.Tags["cpu"] != "cpu-total" {
			return 0, 0, "", false
		}
		idle, ok := s.Floats["usage_idle"]
		return 100 - idle, 0, "", ok
	case store.ResourceLoad:
		if s.M != "system" {
			return 0, 0, "", false
		}
		load, ok1 := s.Floats["load5"]
		n, ok2 := s.Ints["n_cpus"]
		if !ok1 || !ok2 || n <= 0 {
			return 0, 0, "", false
		}
		return load / float64(n), 0, "", true
	}
	return 0, 0, "", false
}

type windowAcc struct {
	count     int
	sum, sumT float64
	min, max  float64
}

func (w *windowAcc) add(v, t float64) {
	if w.count == 0 {
		w.min, w.max = v, v
	}
	w.count++
	w.sum += v
	w.sumT += t
	w.min = math.Min(w.min, v)
	w.max = math.Max(w.max, v)
}

func (w windowAcc) result() store.ResourceWindow {
	if w.count == 0 {
		return store.ResourceWindow{}
	}
	return store.ResourceWindow{Count: w.count, Avg: w.sum / float64(w.count), Min: w.min, Max: w.max, AvgTotal: w.sumT / float64(w.count)}
}

func (m *Mem) HostResourceStats(_ context.Context, measurement string, hostIDs []uuid.UUID, now time.Time) ([]store.ResourceStat, error) {
	switch measurement {
	case store.ResourceMem, store.ResourceDisk, store.ResourceCPU, store.ResourceLoad:
	default:
		return nil, store.ErrUnknownMeasurement
	}
	want := map[uuid.UUID]bool{}
	for _, id := range hostIDs {
		want[id] = true
	}
	type key struct {
		host uuid.UUID
		path string
	}
	type acc struct {
		lastAt              time.Time
		last                float64
		short, medium, long windowAcc
	}
	accs := map[key]*acc{}
	m.mu.Lock()
	for _, s := range m.samples {
		if !want[s.HostID] || !s.T.After(now.Add(-store.ResourceWindowLong)) || s.T.After(now) {
			continue
		}
		v, total, path, ok := resourceValue(measurement, s)
		if !ok {
			continue
		}
		k := key{s.HostID, path}
		a := accs[k]
		if a == nil {
			a = &acc{}
			accs[k] = a
		}
		if a.long.count == 0 || s.T.After(a.lastAt) {
			a.lastAt, a.last = s.T, v
		}
		a.long.add(v, total)
		if s.T.After(now.Add(-store.ResourceWindowMedium)) {
			a.medium.add(v, total)
		}
		if s.T.After(now.Add(-store.ResourceWindowShort)) {
			a.short.add(v, total)
		}
	}
	m.mu.Unlock()
	out := []store.ResourceStat{}
	for k, a := range accs {
		out = append(out, store.ResourceStat{HostID: k.host, Path: k.path, LastAt: a.lastAt, Last: a.last,
			Short: a.short.result(), Medium: a.medium.result(), Long: a.long.result()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostID != out[j].HostID {
			return out[i].HostID.String() < out[j].HostID.String()
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (m *Mem) ListHostDisks(_ context.Context, hostID uuid.UUID, since time.Time) ([]store.HostDisk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	newest := map[string]store.HostDisk{}
	for _, s := range m.samples {
		if s.HostID != hostID || s.M != "disk" || s.T.Before(since) {
			continue
		}
		path := s.Tags["path"]
		if d, ok := newest[path]; ok && !s.T.After(d.At) {
			continue
		}
		d := store.HostDisk{Path: path, Fstype: s.Tags["fstype"], At: s.T}
		if v, ok := s.Ints["total"]; ok {
			d.Total = &v
		}
		if v, ok := s.Ints["used"]; ok {
			d.Used = &v
		}
		if v, ok := s.Floats["used_percent"]; ok {
			d.UsedPercent = &v
		}
		newest[path] = d
	}
	out := []store.HostDisk{}
	for _, d := range newest {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > store.MaxHostDisks {
		out = out[:store.MaxHostDisks]
	}
	return out, nil
}
