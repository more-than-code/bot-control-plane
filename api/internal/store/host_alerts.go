package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Host resource alerts (api-v1 v1.49, migration 0033, spec
// docs/spec-host-resource-alerts.md §3).

// Measurements of HostResourceStats.
const (
	ResourceMem  = "mem"
	ResourceDisk = "disk"
	ResourceCPU  = "cpu"
	ResourceLoad = "load"
)

// ResourceMeasurements is every measurement the sweep reads, in sweep order.
var ResourceMeasurements = []string{ResourceMem, ResourceDisk, ResourceCPU, ResourceLoad}

// Windows of HostResourceStats, ending at now.
const (
	ResourceWindowShort  = 5 * time.Minute
	ResourceWindowMedium = 10 * time.Minute
	ResourceWindowLong   = 15 * time.Minute
)

// Ranges of the per-host overrides (api-v1 v1.49 item 3).
const (
	MemMinAvailablePercentMin = 5
	MemMinAvailablePercentMax = 50
	DiskMaxUsedPercentMin     = 50
	DiskMaxUsedPercentMax     = 99
	CPUMaxBusyPercentMin      = 50
	CPUMaxBusyPercentMax      = 100
)

// HostAlertSettings is one host's alert settings. A nil threshold uses the
// default.
type HostAlertSettings struct {
	AlertsEnabled          bool
	MemMinAvailablePercent *int
	DiskMaxUsedPercent     *int
	CPUMaxBusyPercent      *int
}

// DefaultHostAlertSettings is the settings of a host without a row.
func DefaultHostAlertSettings() HostAlertSettings {
	return HostAlertSettings{AlertsEnabled: true}
}

// ValidHostAlertSettings checks the override ranges.
func ValidHostAlertSettings(s HostAlertSettings) bool {
	in := func(v *int, lo, hi int) bool { return v == nil || (*v >= lo && *v <= hi) }
	return in(s.MemMinAvailablePercent, MemMinAvailablePercentMin, MemMinAvailablePercentMax) &&
		in(s.DiskMaxUsedPercent, DiskMaxUsedPercentMin, DiskMaxUsedPercentMax) &&
		in(s.CPUMaxBusyPercent, CPUMaxBusyPercentMin, CPUMaxBusyPercentMax)
}

// ResourceWindow is the aggregate of one window: the sample count and the
// average, minimum and maximum value. AvgTotal is the average total bytes
// (mem only). All are 0 when Count is 0.
type ResourceWindow struct {
	Count    int
	Avg      float64
	Min      float64
	Max      float64
	AvgTotal float64
}

// ResourceStat is one host's samples of one measurement over the last 15
// minutes (per mount for disk). The value is available bytes (mem),
// used_percent (disk), 100 - usage_idle of cpu-total (cpu) or load5 per
// core (load). Last is the newest sample's value at LastAt.
type ResourceStat struct {
	HostID uuid.UUID
	Path   string
	LastAt time.Time
	Last   float64
	Short  ResourceWindow
	Medium ResourceWindow
	Long   ResourceWindow
}

// MaxHostDisks caps ListHostDisks.
const MaxHostDisks = 200

// HostDisk is one mount a host reported.
type HostDisk struct {
	Path   string
	Fstype string
}

// HostAlertStore keeps the alert settings and reads the sweep's windows.
type HostAlertStore interface {
	// GetHostAlertSettings returns the host's settings, or the defaults
	// when it has no row.
	GetHostAlertSettings(ctx context.Context, hostID uuid.UUID) (HostAlertSettings, error)
	// PutHostAlertSettings stores the host's settings (one row per host).
	// Settings that fail ValidHostAlertSettings are ErrInvalid; a missing
	// host is ErrNotFound.
	PutHostAlertSettings(ctx context.Context, hostID uuid.UUID, s HostAlertSettings, by uuid.UUID, at time.Time) error
	// ListHostAlertSettings returns every stored row by host id. A host
	// without a row is absent and uses the defaults.
	ListHostAlertSettings(ctx context.Context) (map[uuid.UUID]HostAlertSettings, error)
	// HostResourceStats aggregates the samples of measurement (a
	// Resource* constant) in (now-15m, now] for the hosts, in one grouped
	// read: per host (per host and mount for disk), sorted by host id then
	// path. A host without samples is absent. An unknown measurement is
	// ErrUnknownMeasurement.
	HostResourceStats(ctx context.Context, measurement string, hostIDs []uuid.UUID, now time.Time) ([]ResourceStat, error)
	// ListHostDisks returns the mounts the host reported at or after
	// since, sorted by path, at most MaxHostDisks. A mount reported with
	// several fstypes keeps the newest one.
	ListHostDisks(ctx context.Context, hostID uuid.UUID, since time.Time) ([]HostDisk, error)
}
