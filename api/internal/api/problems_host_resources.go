package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// host_resource problems (api-v1 v1.49 items 1 and 2, spec
// docs/spec-host-resource-alerts.md §3), for org owners and admins. Every
// minute the sweep reads one grouped window per measurement for every
// watched host and opens or resolves one problem per host and resource:
//
//	mem   needs_action  opens: avg available < max(15 % of total, 96 MiB) over 10 min
//	                    resolves: avg available >= max(20 % of total, 128 MiB) over 5 min
//	disk  needs_action  opens: used_percent >= 90 for the whole 10 min
//	      (each mount)  resolves: < 85 for 5 min
//	cpu   heads_up      opens: avg busy >= 90 % over 15 min; resolves: < 75 % over 5 min
//	load  heads_up      opens: avg load5 per core >= 2.0 over 15 min; resolves: < 1.2 over 5 min
//
// A per-host override replaces the open point and puts the resolve point
// 5 kinder (memory +5, disk and cpu -5); with a memory override the
// percent alone decides (no MiB floors). For an open problem the resolve
// test runs first; between the two points it stays open. A window opens or
// resolves only with at least half the samples the agent's metrics
// interval gives it (agentws.MetricsIntervalSec), so a host that just came
// back cannot open on a few samples. At most maxMountsPerHost mounts per
// host are judged, by path order.

const (
	memOpenPercent     = 15
	memResolvePercent  = 20
	memOpenFloor       = 96 << 20
	memResolveFloor    = 128 << 20
	diskOpenPercent    = 90
	diskResolveBelow   = 85
	cpuOpenPercent     = 90
	cpuResolveBelow    = 75
	loadOpenPerCore    = 2.0
	loadResolvePerCore = 1.2
	// alertResolveMargin is the gap between an override and its resolve point.
	alertResolveMargin = 5
	// resourceFreshWithin: a host and a measurement are watched only with
	// a report and a sample this recent.
	resourceFreshWithin = 2 * time.Minute
	// resourceSampleEvery is the agent's metrics interval.
	resourceSampleEvery = agentws.MetricsIntervalSec * time.Second
	// maxMountsPerHost caps the mounts judged per host.
	maxMountsPerHost = 50
	// mountLabelMax is the longest mount path shown in problem text.
	mountLabelMax = 64
)

// mountLabel is a mount path for problem text: control and other
// non-printable characters become U+FFFD and it is cut to mountLabelMax
// characters with an ellipsis.
func mountLabel(path string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToValidUTF8(path, "\uFFFD") {
		if n == mountLabelMax {
			b.WriteString("…")
			break
		}
		if !unicode.IsPrint(r) {
			r = '\uFFFD'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// pathHash is the hashed form of a mount in a subject.
func pathHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "#" + hex.EncodeToString(sum[:])[:16]
}

// plainMount reports whether path may stand in a subject as it is:
// printable UTF-8, keeping the subject within store.MaxProblemText.
func plainMount(hostID uuid.UUID, path string) bool {
	if !utf8.ValidString(path) || strings.HasPrefix(path, "#") {
		return false
	}
	for _, r := range path {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return utf8.RuneCountInString(hostID.String()+":disk:"+path) <= store.MaxProblemText
}

func resourceSubject(hostID uuid.UUID, measurement, path string) string {
	if measurement != store.ResourceDisk {
		return hostID.String() + ":" + measurement
	}
	if plainMount(hostID, path) {
		return hostID.String() + ":disk:" + path
	}
	return hostID.String() + ":disk:" + pathHash(path)
}

// parseResourceSubject is the host id, measurement and, for disk, the
// mount part (a path, or "#" and a hash) of a subject.
func parseResourceSubject(subject string) (uuid.UUID, string, string) {
	head, rest, _ := strings.Cut(subject, ":")
	id, err := uuid.Parse(head)
	if err != nil {
		return uuid.Nil, "", ""
	}
	m, mount, _ := strings.Cut(rest, ":")
	return id, m, mount
}

// windowMinutes is a window as text, for example "10 min".
func windowMinutes(d time.Duration) string {
	return fmt.Sprintf("%d min", int(d/time.Minute))
}

// covered: the window holds at least half the samples the metrics
// interval gives it.
func covered(w store.ResourceWindow, span time.Duration) bool {
	return w.Count >= int(span/resourceSampleEvery)/2
}

func bytesText(b float64) string {
	const gib = 1 << 30
	switch {
	case b >= 10*gib:
		return fmt.Sprintf("%.0f GiB", b/gib)
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", b/gib)
	}
	return fmt.Sprintf("%.0f MiB", b/(1<<20))
}

func percentOf(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return part / whole * 100
}

// resourceVerdict is what one stat says: open (with text), resolve (with
// text), or neither.
type resourceVerdict struct {
	open, resolve bool
	level         string
	title, detail string
}

func memText(w store.ResourceWindow, span time.Duration) string {
	return fmt.Sprintf("%s available of %s (%.0f %%), %s average.", bytesText(w.Avg), bytesText(w.AvgTotal), percentOf(w.Avg, w.AvgTotal), windowMinutes(span))
}

// judgeResource applies the thresholds to one stat of host. isOpen is
// whether the subject has an open problem: then the resolve test runs
// first, so a recovered resource resolves even while the longer open
// window still averages in the bad minutes.
func judgeResource(measurement string, st store.ResourceStat, host string, set store.HostAlertSettings, isOpen bool) resourceVerdict {
	var v resourceVerdict
	var openNow, resolveNow bool
	var openTitle, openDetail, resolveTitle, resolveDetail string
	short, medium, long := store.ResourceWindowShort, store.ResourceWindowMedium, store.ResourceWindowLong
	switch measurement {
	case store.ResourceMem:
		v.level = store.ProblemNeedsAction
		openPct, resolvePct := float64(memOpenPercent), float64(memResolvePercent)
		openFloor, resolveFloor := float64(memOpenFloor), float64(memResolveFloor)
		if set.MemMinAvailablePercent != nil {
			// With an override the percent alone decides.
			openPct = float64(*set.MemMinAvailablePercent)
			resolvePct = openPct + alertResolveMargin
			openFloor, resolveFloor = 0, 0
		}
		m, s := st.Medium, st.Short
		openNow = covered(m, medium) && m.Avg < math.Max(openPct/100*m.AvgTotal, openFloor)
		resolveNow = covered(s, short) && s.Avg >= math.Max(resolvePct/100*s.AvgTotal, resolveFloor)
		openTitle, openDetail = host+" is low on memory", memText(m, medium)
		resolveTitle, resolveDetail = host+" has enough memory again", memText(s, short)
	case store.ResourceDisk:
		v.level = store.ProblemNeedsAction
		openAt, resolveBelow := float64(diskOpenPercent), float64(diskResolveBelow)
		if set.DiskMaxUsedPercent != nil {
			openAt = float64(*set.DiskMaxUsedPercent)
			resolveBelow = openAt - alertResolveMargin
		}
		m, s := st.Medium, st.Short
		// The newest sample is in the medium window (it is under 2 minutes
		// old), so the window minimum covers it.
		openNow = covered(m, medium) && m.Min >= openAt
		resolveNow = covered(s, short) && s.Max < resolveBelow
		title := fmt.Sprintf("%s disk %s is %.0f %% full", host, mountLabel(st.Path), st.Last)
		openTitle, openDetail = title, fmt.Sprintf("At or above %.0f %% for %s.", openAt, windowMinutes(medium))
		resolveTitle, resolveDetail = title, fmt.Sprintf("Below %.0f %% for %s.", resolveBelow, windowMinutes(short))
	case store.ResourceCPU:
		v.level = store.ProblemHeadsUp
		openAt, resolveBelow := float64(cpuOpenPercent), float64(cpuResolveBelow)
		if set.CPUMaxBusyPercent != nil {
			openAt = float64(*set.CPUMaxBusyPercent)
			resolveBelow = openAt - alertResolveMargin
		}
		l, s := st.Long, st.Short
		openNow = covered(l, long) && l.Avg >= openAt
		resolveNow = covered(s, short) && s.Avg < resolveBelow
		openTitle, openDetail = host+" CPU is busy", fmt.Sprintf("%.0f %% busy, %s average.", l.Avg, windowMinutes(long))
		resolveTitle, resolveDetail = host+" CPU is back to normal", fmt.Sprintf("%.0f %% busy, %s average.", s.Avg, windowMinutes(short))
	case store.ResourceLoad:
		v.level = store.ProblemHeadsUp
		l, s := st.Long, st.Short
		openNow = covered(l, long) && l.Avg >= loadOpenPerCore
		resolveNow = covered(s, short) && s.Avg < loadResolvePerCore
		openTitle, openDetail = host+" is overloaded", fmt.Sprintf("Load %.1f per core, %s average.", l.Avg, windowMinutes(long))
		resolveTitle, resolveDetail = host+" load is back to normal", fmt.Sprintf("Load %.1f per core, %s average.", s.Avg, windowMinutes(short))
	default:
		return v
	}
	switch {
	case isOpen && resolveNow:
		v.resolve, v.title, v.detail = true, resolveTitle, resolveDetail
	case openNow:
		v.open, v.title, v.detail = true, openTitle, openDetail
	}
	return v
}

// sweepHostResources is the host_resource check. Watched hosts are always
// on, not revoked, reported within 2 minutes and have alerts on; a
// measurement is judged only with a sample within 2 minutes. Afterwards
// every open problem the sweep did not judge resolves ("No longer
// reported" for a mount that is gone, "No longer checked" otherwise),
// unless its measurement could not be read this minute.
func (s *Server) sweepHostResources(ctx context.Context, now time.Time) {
	hosts, err := s.Store.ListProblemHosts(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostResource)
		return
	}
	settings, err := s.Store.ListHostAlertSettings(ctx)
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostResource)
		return
	}
	rows, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemHostResource})
	if err != nil {
		s.logger().Error("problem sweep failed", "kind", store.ProblemHostResource)
		return
	}
	open := map[string]store.Problem{}
	for _, p := range rows {
		if p.ResolvedAt == nil {
			open[p.Subject] = p
		}
	}
	settingsFor := func(id uuid.UUID) store.HostAlertSettings {
		if set, found := settings[id]; found {
			return set
		}
		return store.DefaultHostAlertSettings()
	}
	why := map[uuid.UUID]string{} // why a live host is not watched
	watched := map[uuid.UUID]store.ProblemHost{}
	for _, h := range hosts {
		set := settingsFor(h.ID)
		switch {
		case !h.AlwaysOn:
			why[h.ID] = "the host is not always on."
		case h.LastSeenAt == nil || now.Sub(*h.LastSeenAt) >= resourceFreshWithin:
			why[h.ID] = "the host is not reporting."
		case !set.AlertsEnabled:
			why[h.ID] = "alerts are off for this host."
		default:
			watched[h.ID] = h
		}
	}
	ids := make([]uuid.UUID, 0, len(watched))
	for id := range watched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	judged := map[string]bool{}
	unread := map[string]bool{}
	if len(ids) > 0 {
		for _, m := range store.ResourceMeasurements {
			stats, err := s.Store.HostResourceStats(ctx, m, ids, now)
			if err != nil {
				s.logger().Error("problem sweep failed", "kind", store.ProblemHostResource)
				unread[m] = true
				continue
			}
			mounts := map[uuid.UUID]int{}
			for _, st := range stats {
				h, isWatched := watched[st.HostID]
				if !isWatched || now.Sub(st.LastAt) >= resourceFreshWithin {
					continue
				}
				if m == store.ResourceDisk {
					// Stats come sorted by host then path: the first mounts win.
					if mounts[h.ID] >= maxMountsPerHost {
						continue
					}
					mounts[h.ID]++
				}
				subject := resourceSubject(h.ID, m, st.Path)
				judged[subject] = true
				_, isOpen := open[subject]
				v := judgeResource(m, st, h.Name, settingsFor(h.ID), isOpen)
				switch {
				case v.open:
					id := h.ID
					s.openProblem(ctx, store.ProblemOpen{
						Kind: store.ProblemHostResource, Subject: subject, OrgID: h.OrgID, HostID: &id,
						Level: v.level, Title: v.title, Detail: v.detail,
					})
				case v.resolve:
					s.resolveProblem(ctx, store.ProblemHostResource, subject, v.title, v.detail)
				}
			}
		}
	}
	for subject := range open {
		if judged[subject] {
			continue
		}
		hostID, m, _ := parseResourceSubject(subject)
		if unread[m] {
			continue
		}
		detail := "No longer checked: the host was revoked."
		switch {
		case why[hostID] != "":
			detail = "No longer checked: " + why[hostID]
		case watched[hostID].ID != uuid.Nil && m == store.ResourceDisk:
			// Spec §7.3: a mount that is gone (or past the per-host cap).
			detail = "No longer reported: the host no longer reports this mount."
		case watched[hostID].ID != uuid.Nil:
			detail = "No longer checked: no recent samples."
		}
		s.resolveProblem(ctx, store.ProblemHostResource, subject, "", detail)
	}
}

// hostResourceHref is the host page over the last hour (api-v1 v1.49 item
// 1); a disk problem adds its mount (&disk=, escaped) when the path is known.
func hostResourceHref(hostID uuid.UUID, diskPath string) string {
	href := "/hosts/" + hostID.String() + "?range=1h"
	if diskPath != "" {
		href += "&disk=" + url.QueryEscape(diskPath)
	}
	return href
}

// resourceHref is the link of a host_resource problem. A hashed disk
// subject is matched against the host's mounts of the last 24 hours (read
// once per host per request); without a match the link has no mount.
func (r *problemReader) resourceHref(ctx context.Context, p store.Problem) string {
	hostID, m, mount := parseResourceSubject(p.Subject)
	if p.HostID != nil {
		hostID = *p.HostID
	}
	if m != store.ResourceDisk || mount == "" {
		return hostResourceHref(hostID, "")
	}
	if !strings.HasPrefix(mount, "#") {
		return hostResourceHref(hostID, mount)
	}
	if r.disks == nil {
		r.disks = map[uuid.UUID][]store.HostDisk{}
	}
	disks, ok := r.disks[hostID]
	if !ok {
		disks, _ = r.s.Store.ListHostDisks(ctx, hostID, r.s.now().Add(-hostDisksSince))
		r.disks[hostID] = disks
	}
	for _, d := range disks {
		if pathHash(d.Path) == mount {
			return hostResourceHref(hostID, d.Path)
		}
	}
	return hostResourceHref(hostID, "")
}
