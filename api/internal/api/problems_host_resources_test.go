package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.49 (spec-host-resource-alerts §8): the host_resource sweep.

const mib = 1 << 20

// resScene is one org with an always-on host, the sweep past its grace and
// a settable clock at w.now.
type resScene struct {
	w     *world
	set   func(time.Time)
	at    time.Time
	owner store.User
	p     store.Project
	host  store.Host
}

func newResScene(t *testing.T) *resScene {
	t.Helper()
	w := newWorld(t)
	set := problemClock(w)
	owner := w.user("owner@ex.com", "Owner")
	p, err := w.mem.CreateProject(context.Background(), w.mem.PersonalOrg(owner.ID), owner.ID, "Proj", "proj", w.now)
	if err != nil {
		t.Fatal(err)
	}
	host := enrollAt(t, w, p, owner.ID, "pi1", "0.2.9")
	w.srv.sweep.start = w.now.Add(-time.Hour)
	return &resScene{w: w, set: set, at: w.now, owner: owner, p: p, host: host}
}

// feed stores one sample every 10 s in [from, to] for host, built by mk.
func (r *resScene) feed(t *testing.T, host store.Host, from, to time.Time, mk func(at time.Time) []store.Sample) {
	t.Helper()
	var samples []store.Sample
	for at := from; !at.After(to); at = at.Add(10 * time.Second) {
		samples = append(samples, mk(at)...)
	}
	if _, err := r.w.mem.InsertBatch(context.Background(), host.ID, uuid.New(), samples, to); err != nil {
		t.Fatal(err)
	}
}

func memSample(avail, total int64) func(time.Time) []store.Sample {
	return func(at time.Time) []store.Sample {
		return []store.Sample{{M: "mem", T: at, Ints: map[string]int64{"available": avail, "total": total, "used": total - avail}, Floats: map[string]float64{"used_percent": 1}}}
	}
}

func diskSample(path string, used float64) func(time.Time) []store.Sample {
	return func(at time.Time) []store.Sample {
		return []store.Sample{{M: "disk", T: at, Tags: map[string]string{"path": path, "fstype": "ext4"}, Floats: map[string]float64{"used_percent": used}}}
	}
}

func cpuSample(busy float64) func(time.Time) []store.Sample {
	return func(at time.Time) []store.Sample {
		return []store.Sample{{M: "cpu", T: at, Tags: map[string]string{"cpu": "cpu-total"}, Floats: map[string]float64{"usage_idle": 100 - busy}}}
	}
}

func loadSample(load5 float64, cpus int64) func(time.Time) []store.Sample {
	return func(at time.Time) []store.Sample {
		return []store.Sample{{M: "system", T: at, Floats: map[string]float64{"load5": load5}, Ints: map[string]int64{"n_cpus": cpus}}}
	}
}

// sweepAt moves the clock to at, marks the host seen 10 s before and sweeps.
func (r *resScene) sweepAt(t *testing.T, at time.Time, hosts ...store.Host) {
	t.Helper()
	r.at = at
	r.set(at)
	if len(hosts) == 0 {
		hosts = []store.Host{r.host}
	}
	for _, h := range hosts {
		touchAt(t, r.w, h, at.Add(-10*time.Second), "")
	}
	sweepNow(context.Background(), r.w.srv)
}

func (r *resScene) problem(t *testing.T, subject string) (store.Problem, bool) {
	t.Helper()
	return problemOf(t, r.w, store.ProblemHostResource, subject)
}

func (r *resScene) subject(suffix string) string { return r.host.ID.String() + ":" + suffix }

func (r *resScene) settings(t *testing.T, s store.HostAlertSettings) {
	t.Helper()
	if err := r.w.mem.PutHostAlertSettings(context.Background(), r.host.ID, s, r.owner.ID, r.at); err != nil {
		t.Fatal(err)
	}
}

// TestHostResourceMemory is acceptance 1 and 2: 10 minutes low opens one
// needs_action problem with numbers only; a value between the open and
// resolve points keeps it open; 5 minutes recovered resolves it.
func TestHostResourceMemory(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-12*time.Minute), t0, memSample(87*mib, 768*mib))
	r.sweepAt(t, t0)
	p, ok := r.problem(t, r.subject("mem"))
	if !ok || p.Level != store.ProblemNeedsAction || p.Title != "pi1 is low on memory" ||
		p.Detail != "87 MiB available of 768 MiB (11 %), 10 min average." || p.ResolvedAt != nil || p.HostID == nil || *p.HostID != r.host.ID {
		t.Fatalf("open %+v %v", p, ok)
	}
	// 17 % for 6 minutes: above the open point, below the resolve point (20 %).
	t1 := t0.Add(6 * time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, memSample(131*mib, 768*mib))
	r.sweepAt(t, t1)
	if p, _ := r.problem(t, r.subject("mem")); p.ResolvedAt != nil {
		t.Fatalf("resolved in the band %+v", p)
	}
	// 25 % for 6 minutes: resolved.
	t2 := t1.Add(6 * time.Minute)
	r.feed(t, r.host, t1.Add(10*time.Second), t2, memSample(192*mib, 768*mib))
	r.sweepAt(t, t2)
	p, _ = r.problem(t, r.subject("mem"))
	if p.ResolvedAt == nil || p.Title != "pi1 has enough memory again" || p.Detail != "192 MiB available of 768 MiB (25 %), 5 min average." {
		t.Fatalf("resolved %+v", p)
	}
	if n := len(allProblems(t, r.w)); n != 1 {
		t.Fatalf("problems %d", n)
	}
	assertNoSecrets(t, r.w, "owner@ex.com")
}

// TestHostResourceTenMinuteRule is acceptance 2: a dip shorter than 10
// minutes does not open, nor does a window with too few samples; the floor
// of 96 MiB applies on a small host.
func TestHostResourceTenMinuteRule(t *testing.T) {
	for name, tc := range map[string]struct {
		feed func(r *resScene, t *testing.T, t0 time.Time)
		open bool
	}{
		"4 min dip": {func(r *resScene, t *testing.T, t0 time.Time) {
			r.feed(t, r.host, t0.Add(-15*time.Minute), t0.Add(-4*time.Minute), memSample(400*mib, 768*mib))
			r.feed(t, r.host, t0.Add(-230*time.Second), t0, memSample(10*mib, 768*mib))
		}, false},
		"only 4 min of samples": {func(r *resScene, t *testing.T, t0 time.Time) {
			r.feed(t, r.host, t0.Add(-4*time.Minute), t0, memSample(10*mib, 768*mib))
		}, false},
		"10 min low": {func(r *resScene, t *testing.T, t0 time.Time) {
			r.feed(t, r.host, t0.Add(-10*time.Minute), t0, memSample(10*mib, 768*mib))
		}, true},
		"small host under the floor": {func(r *resScene, t *testing.T, t0 time.Time) {
			// 90 of 512 MiB is 17 %, above 15 %, but under 96 MiB.
			r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(90*mib, 512*mib))
		}, true},
		"large host above 15 %": {func(r *resScene, t *testing.T, t0 time.Time) {
			r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(2048*mib, 8192*mib))
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newResScene(t)
			tc.feed(r, t, r.w.now)
			r.sweepAt(t, r.w.now)
			if _, ok := r.problem(t, r.subject("mem")); ok != tc.open {
				t.Fatalf("open %v want %v", ok, tc.open)
			}
		})
	}
}

// TestHostResourceDisk is acceptance 3: per mount, the latest sample and
// 10 minutes at 90 %, resolved under 85 % for 5 minutes, and a mount that
// is no longer reported resolves with "No longer checked".
func TestHostResourceDisk(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-12*time.Minute), t0, diskSample("/", 92))
	r.feed(t, r.host, t0.Add(-12*time.Minute), t0, diskSample("/data", 40))
	r.feed(t, r.host, t0.Add(-12*time.Minute), t0, diskSample("/var", 95))
	// /var dropped under 90 for one sample 5 minutes ago: not 10 minutes.
	r.feed(t, r.host, t0.Add(-5*time.Minute), t0.Add(-5*time.Minute), diskSample("/var", 70))
	r.sweepAt(t, t0)
	p, ok := r.problem(t, r.subject("disk:/"))
	if !ok || p.Level != store.ProblemNeedsAction || p.Title != "pi1 disk / is 92 % full" || p.Detail != "At or above 90 % for 10 min." {
		t.Fatalf("/ %+v %v", p, ok)
	}
	for _, path := range []string{"/data", "/var"} {
		if _, ok := r.problem(t, r.subject("disk:"+path)); ok {
			t.Fatalf("%s opened", path)
		}
	}
	// 87 % (in the band) for 6 minutes: still open. Then 80 %: resolved.
	t1 := t0.Add(6 * time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, diskSample("/", 87))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, diskSample("/data", 40))
	r.sweepAt(t, t1)
	if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt != nil {
		t.Fatal("resolved in the band")
	}
	t2 := t1.Add(6 * time.Minute)
	r.feed(t, r.host, t1.Add(10*time.Second), t2, diskSample("/", 80))
	r.feed(t, r.host, t1.Add(10*time.Second), t2, diskSample("/data", 40))
	r.sweepAt(t, t2)
	if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt == nil || p.Title != "pi1 disk / is 80 % full" || p.Detail != "Below 85 % for 5 min." {
		t.Fatalf("resolved %+v", p)
	}
	// /data fills up and opens, then disappears (only / is reported).
	t3 := t2.Add(11 * time.Minute)
	r.feed(t, r.host, t2.Add(10*time.Second), t3, diskSample("/data", 99))
	r.feed(t, r.host, t2.Add(10*time.Second), t3, diskSample("/", 80))
	r.sweepAt(t, t3)
	if p, ok := r.problem(t, r.subject("disk:/data")); !ok || p.ResolvedAt != nil {
		t.Fatalf("/data %+v %v", p, ok)
	}
	t4 := t3.Add(3 * time.Minute)
	r.feed(t, r.host, t3.Add(10*time.Second), t4, diskSample("/", 80))
	r.sweepAt(t, t4)
	p, _ = r.problem(t, r.subject("disk:/data"))
	if p.ResolvedAt == nil || p.Detail != "No longer reported: the host no longer reports this mount." || p.Title != "pi1 disk /data is 99 % full" {
		t.Fatalf("gone mount %+v", p)
	}
}

// TestHostResourceCPULoad is acceptance 4: cpu and load are heads_up.
func TestHostResourceCPULoad(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, cpuSample(95))
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, loadSample(10, 4))
	r.sweepAt(t, t0)
	cpu, ok1 := r.problem(t, r.subject("cpu"))
	load, ok2 := r.problem(t, r.subject("load"))
	if !ok1 || cpu.Level != store.ProblemHeadsUp || cpu.Title != "pi1 CPU is busy" || cpu.Detail != "95 % busy, 15 min average." {
		t.Fatalf("cpu %+v", cpu)
	}
	if !ok2 || load.Level != store.ProblemHeadsUp || load.Title != "pi1 is overloaded" || load.Detail != "Load 2.5 per core, 15 min average." {
		t.Fatalf("load %+v", load)
	}
	// 80 % busy and 1.5 per core: in the band, open. Then 50 % and 0.5: resolved.
	t1 := t0.Add(6 * time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, cpuSample(80))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, loadSample(6, 4))
	r.sweepAt(t, t1)
	for _, s := range []string{"cpu", "load"} {
		if p, _ := r.problem(t, r.subject(s)); p.ResolvedAt != nil {
			t.Fatalf("%s resolved in the band", s)
		}
	}
	t2 := t1.Add(6 * time.Minute)
	r.feed(t, r.host, t1.Add(10*time.Second), t2, cpuSample(50))
	r.feed(t, r.host, t1.Add(10*time.Second), t2, loadSample(2, 4))
	r.sweepAt(t, t2)
	if p, _ := r.problem(t, r.subject("cpu")); p.ResolvedAt == nil || p.Title != "pi1 CPU is back to normal" || p.Detail != "50 % busy, 5 min average." {
		t.Fatalf("cpu resolved %+v", p)
	}
	if p, _ := r.problem(t, r.subject("load")); p.ResolvedAt == nil || p.Detail != "Load 0.5 per core, 5 min average." {
		t.Fatalf("load resolved %+v", p)
	}
}

// TestHostResourceNotWatched is acceptance 5: offline, asleep, revoked,
// stale and switched-off hosts open nothing, and their open problems
// resolve with "No longer checked".
func TestHostResourceNotWatched(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(r *resScene, t *testing.T, at time.Time)
		reason string
	}{
		"offline": {func(r *resScene, t *testing.T, at time.Time) {
			touchAt(t, r.w, r.host, at.Add(-3*time.Minute), "")
		}, "No longer checked: the host is not reporting."},
		"stale samples": {nil, "No longer checked: no recent samples."},
		"alerts off": {func(r *resScene, t *testing.T, at time.Time) {
			r.settings(t, store.HostAlertSettings{AlertsEnabled: false})
		}, "No longer checked: alerts are off for this host."},
		"revoked": {func(r *resScene, t *testing.T, at time.Time) {
			if err := r.w.mem.RevokeHost(context.Background(), r.host.ID, at); err != nil {
				t.Fatal(err)
			}
		}, "No longer checked: the host was revoked."},
	} {
		t.Run(name, func(t *testing.T) {
			r := newResScene(t)
			t0 := r.w.now
			r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(10*mib, 768*mib))
			r.sweepAt(t, t0)
			if _, ok := r.problem(t, r.subject("mem")); !ok {
				t.Fatal("not opened")
			}
			// 3 minutes later, no new samples (stale), and the change.
			t1 := t0.Add(3 * time.Minute)
			r.set(t1)
			touchAt(t, r.w, r.host, t1.Add(-10*time.Second), "")
			if tc.change != nil {
				tc.change(r, t, t1)
			}
			sweepNow(context.Background(), r.w.srv)
			p, _ := r.problem(t, r.subject("mem"))
			if p.ResolvedAt == nil || p.Detail != tc.reason || p.Title != "pi1 is low on memory" {
				t.Fatalf("%+v want %q", p, tc.reason)
			}
		})
	}
	// Opens nothing: an asleep host, a host seen 3 minutes ago, alerts off.
	r := newResScene(t)
	t0 := r.w.now
	asleep := enrollAsleep(t, r.w, r.p, r.owner.ID, "laptop")
	quiet := enrollAt(t, r.w, r.p, r.owner.ID, "quiet", "0.2.9")
	for _, h := range []store.Host{asleep, quiet, r.host} {
		r.feed(t, h, t0.Add(-11*time.Minute), t0, memSample(10*mib, 768*mib))
	}
	r.settings(t, store.HostAlertSettings{AlertsEnabled: false})
	touchAt(t, r.w, asleep, t0.Add(-10*time.Second), "")
	touchAt(t, r.w, quiet, t0.Add(-3*time.Minute), "")
	r.set(t0)
	touchAt(t, r.w, r.host, t0.Add(-10*time.Second), "")
	sweepNow(context.Background(), r.w.srv)
	if n := len(allProblems(t, r.w)); n != 0 {
		t.Fatalf("problems %+v", allProblems(t, r.w))
	}
}

// TestHostResourceOverrides is acceptance 6: overrides take effect at the
// next sweep, with the resolve point 5 points kinder.
func TestHostResourceOverrides(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	// 25 % available, 92 % disk, 70 % cpu: nothing by default.
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, memSample(256*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, diskSample("/", 92))
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, cpuSample(70))
	r.settings(t, store.HostAlertSettings{AlertsEnabled: true, DiskMaxUsedPercent: intp(95)})
	r.sweepAt(t, t0)
	if n := len(allProblems(t, r.w)); n != 0 {
		t.Fatalf("defaults opened %+v", allProblems(t, r.w))
	}
	r.settings(t, store.HostAlertSettings{AlertsEnabled: true, MemMinAvailablePercent: intp(30), DiskMaxUsedPercent: intp(90), CPUMaxBusyPercent: intp(60)})
	t1 := t0.Add(time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, memSample(256*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, diskSample("/", 92))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, cpuSample(70))
	r.sweepAt(t, t1)
	for _, s := range []string{"mem", "disk:/", "cpu"} {
		if p, ok := r.problem(t, r.subject(s)); !ok || p.ResolvedAt != nil {
			t.Fatalf("%s not opened", s)
		}
	}
	// mem 33 % (< 35), cpu 57 % (> 55): in the band. mem 36 %, cpu 50 %: resolved.
	t2 := t1.Add(6 * time.Minute)
	r.feed(t, r.host, t1.Add(10*time.Second), t2, memSample(338*mib, 1024*mib))
	r.feed(t, r.host, t1.Add(10*time.Second), t2, cpuSample(57))
	r.feed(t, r.host, t1.Add(10*time.Second), t2, diskSample("/", 92))
	r.sweepAt(t, t2)
	for _, s := range []string{"mem", "cpu"} {
		if p, _ := r.problem(t, r.subject(s)); p.ResolvedAt != nil {
			t.Fatalf("%s resolved in the band", s)
		}
	}
	t3 := t2.Add(6 * time.Minute)
	r.feed(t, r.host, t2.Add(10*time.Second), t3, memSample(369*mib, 1024*mib))
	r.feed(t, r.host, t2.Add(10*time.Second), t3, cpuSample(50))
	r.feed(t, r.host, t2.Add(10*time.Second), t3, diskSample("/", 92))
	r.sweepAt(t, t3)
	for _, s := range []string{"mem", "cpu"} {
		if p, _ := r.problem(t, r.subject(s)); p.ResolvedAt == nil {
			t.Fatalf("%s not resolved", s)
		}
	}
	// The disk stays at 92 %, above its override of 90: still open.
	if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt != nil {
		t.Fatal("disk resolved")
	}
}

// TestHostResourceGraceAndOff is acceptance 8.
func TestHostResourceGraceAndOff(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(10*mib, 768*mib))
	r.w.srv.sweep.start = t0.Add(-4 * time.Minute)
	r.sweepAt(t, t0)
	if n := len(allProblems(t, r.w)); n != 0 {
		t.Fatal("opened in the grace period")
	}
	r.w.srv.sweep.start = t0.Add(-time.Hour)
	r.w.srv.ProblemsOff = true
	r.sweepAt(t, t0)
	if n := len(allProblems(t, r.w)); n != 0 {
		t.Fatal("opened with problems off")
	}
}

// countStats counts HostResourceStats calls.
type countStats struct {
	store.Store
	mu    sync.Mutex
	calls []string
	hosts []int
}

func (c *countStats) HostResourceStats(ctx context.Context, m string, ids []uuid.UUID, now time.Time) ([]store.ResourceStat, error) {
	c.mu.Lock()
	c.calls = append(c.calls, m)
	c.hosts = append(c.hosts, len(ids))
	c.mu.Unlock()
	return c.Store.HostResourceStats(ctx, m, ids, now)
}

// TestHostResourceOneQueryPerMeasurement is acceptance 7: one grouped read
// per measurement, whatever the number of hosts.
func TestHostResourceOneQueryPerMeasurement(t *testing.T) {
	r := newResScene(t)
	hosts := []store.Host{r.host}
	for _, name := range []string{"pi2", "pi3", "pi4", "us1"} {
		hosts = append(hosts, enrollAt(t, r.w, r.p, r.owner.ID, name, "0.2.9"))
	}
	for _, h := range hosts {
		r.feed(t, h, r.w.now.Add(-11*time.Minute), r.w.now, memSample(10*mib, 768*mib))
	}
	c := &countStats{Store: r.w.mem}
	r.w.srv.Store = c
	r.sweepAt(t, r.w.now, hosts...)
	if strings.Join(c.calls, ",") != "mem,disk,cpu,load" {
		t.Fatalf("calls %v", c.calls)
	}
	for _, n := range c.hosts {
		if n != len(hosts) {
			t.Fatalf("hosts per call %v", c.hosts)
		}
	}
	opened := 0
	for _, p := range allProblems(t, r.w) {
		if p.Kind == store.ProblemHostResource {
			opened++
		}
	}
	if opened != len(hosts) {
		t.Fatalf("opened %d", opened)
	}
}

// TestHostResourceAudience: org owners and admins see host_resource with
// the host link over the last hour; project members do not.
func TestHostResourceAudience(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	set := problemClock(w)
	w.srv.sweep.start = w.now.Add(-time.Hour)
	host := fx.sc.host
	var samples []store.Sample
	for at := w.now.Add(-11 * time.Minute); !at.After(w.now); at = at.Add(10 * time.Second) {
		samples = append(samples, memSample(10*mib, 768*mib)(at)...)
	}
	if _, err := w.mem.InsertBatch(context.Background(), host.ID, uuid.New(), samples, w.now); err != nil {
		t.Fatal(err)
	}
	set(w.now)
	touchAt(t, w, host, w.now.Add(-10*time.Second), "")
	sweepNow(context.Background(), w.srv)
	org := "/v1/orgs/" + fx.home.OrgID.String() + "/problems"
	for _, actor := range []string{actOwnerOwner, actOwnerAdmin} {
		items := getProblems(t, w, fx.tok[actor], org)
		if len(items) != 1 || items[0].Kind != store.ProblemHostResource || items[0].Href != "/hosts/"+host.ID.String()+"?range=1h" || items[0].Level != store.ProblemNeedsAction {
			t.Fatalf("%s: %+v", actor, items)
		}
	}
	for _, actor := range []string{actOwnerViewer, actDeployAdmin, actMetricsAdmin} {
		for _, p := range []string{fx.home.ID.String(), fx.deploy.ID.String(), fx.metrics.ID.String()} {
			status, env, raw := mustCall(t, w, fx.tok[actor], http.MethodGet, "/v1/projects/"+p+"/problems", "")
			if status == http.StatusOK && strings.Contains(raw, store.ProblemHostResource) {
				t.Fatalf("%s sees host_resource in %s: %s %v", actor, p, raw, env)
			}
		}
	}
}

func intp(v int) *int { return &v }

// TestHostResourceResolveFirst (review item 3): an open problem resolves
// after 5 minutes of recovery even while the longer open window still
// averages in the bad minutes.
func TestHostResourceResolveFirst(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	// Memory 2 % for 12 minutes, CPU 100 % for 15 minutes: both open.
	r.feed(t, r.host, t0.Add(-12*time.Minute), t0, memSample(20*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(-15*time.Minute), t0, cpuSample(100))
	r.sweepAt(t, t0)
	for _, s := range []string{"mem", "cpu"} {
		if p, ok := r.problem(t, r.subject(s)); !ok || p.ResolvedAt != nil {
			t.Fatalf("%s not open", s)
		}
	}
	// Then 22 % available and 74 % busy. After 4 minutes the 5-minute
	// window is still mixed: open.
	t1 := t0.Add(4 * time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, memSample(225*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, cpuSample(74))
	r.sweepAt(t, t1)
	for _, s := range []string{"mem", "cpu"} {
		if p, _ := r.problem(t, r.subject(s)); p.ResolvedAt != nil {
			t.Fatalf("%s resolved before 5 minutes", s)
		}
	}
	// At 5 minutes of recovery both resolve, although the 10-minute memory
	// average (12 %) and the 15-minute CPU average (91 %) are still bad.
	t2 := t0.Add(5 * time.Minute)
	r.feed(t, r.host, t1.Add(10*time.Second), t2, memSample(225*mib, 1024*mib))
	r.feed(t, r.host, t1.Add(10*time.Second), t2, cpuSample(74))
	r.sweepAt(t, t2)
	for _, s := range []string{"mem", "cpu"} {
		if p, _ := r.problem(t, r.subject(s)); p.ResolvedAt == nil {
			t.Fatalf("%s not resolved after 5 minutes of recovery", s)
		}
	}
}

// TestHostResourceStaleMeasurement (review item 9): a host reporting now
// whose memory samples stopped 3 minutes ago opens nothing.
func TestHostResourceStaleMeasurement(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-14*time.Minute), t0.Add(-3*time.Minute), memSample(10*mib, 1024*mib))
	r.sweepAt(t, t0)
	if n := len(allProblems(t, r.w)); n != 0 {
		t.Fatalf("stale measurement opened %+v", allProblems(t, r.w))
	}
}

// failStats fails HostResourceStats for one measurement.
type failStats struct {
	store.Store
	fail string
}

func (f failStats) HostResourceStats(ctx context.Context, m string, ids []uuid.UUID, now time.Time) ([]store.ResourceStat, error) {
	if m == f.fail {
		return nil, errors.New("database: down")
	}
	return f.Store.HostResourceStats(ctx, m, ids, now)
}

// TestHostResourceReadFailure (review item 10): a failed mem read keeps the
// open mem problem; disk is judged as usual.
func TestHostResourceReadFailure(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(10*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(-11*time.Minute), t0, diskSample("/", 95))
	r.sweepAt(t, t0)
	for _, s := range []string{"mem", "disk:/"} {
		if _, ok := r.problem(t, r.subject(s)); !ok {
			t.Fatalf("%s not open", s)
		}
	}
	r.w.srv.Store = failStats{Store: r.w.mem, fail: store.ResourceMem}
	t1 := t0.Add(6 * time.Minute)
	r.feed(t, r.host, t0.Add(10*time.Second), t1, memSample(800*mib, 1024*mib))
	r.feed(t, r.host, t0.Add(10*time.Second), t1, diskSample("/", 50))
	r.sweepAt(t, t1)
	if p, _ := r.problem(t, r.subject("mem")); p.ResolvedAt != nil {
		t.Fatalf("mem resolved on a failed read: %+v", p)
	}
	if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt == nil || p.Detail != "Below 85 % for 5 min." {
		t.Fatalf("disk not resolved: %+v", p)
	}
}

// TestHostResourceCoverageAndFloors (review items 7 and 11): the resolve
// side needs half its samples too; the MiB floors apply only without a
// memory override; a disk override of 90 resolves under 85.
func TestHostResourceCoverageAndFloors(t *testing.T) {
	t.Run("resolve needs half the samples", func(t *testing.T) {
		r := newResScene(t)
		t0 := r.w.now
		r.feed(t, r.host, t0.Add(-11*time.Minute), t0, memSample(10*mib, 1024*mib))
		r.sweepAt(t, t0)
		// 14 samples (under half of 30) of good memory in the last 5 minutes.
		t1 := t0.Add(5 * time.Minute)
		r.feed(t, r.host, t1.Add(-130*time.Second), t1, memSample(800*mib, 1024*mib))
		r.sweepAt(t, t1)
		if p, _ := r.problem(t, r.subject("mem")); p.ResolvedAt != nil {
			t.Fatal("resolved on 14 samples")
		}
		// One more sample makes 15: resolved.
		r.feed(t, r.host, t1.Add(10*time.Second), t1.Add(10*time.Second), memSample(800*mib, 1024*mib))
		r.sweepAt(t, t1.Add(10*time.Second))
		if p, _ := r.problem(t, r.subject("mem")); p.ResolvedAt == nil {
			t.Fatal("not resolved on 15 samples")
		}
	})
	t.Run("floor without an override, percent alone with one", func(t *testing.T) {
		// 90 of 512 MiB is 17.6 %: above 15 %, under the 96 MiB floor.
		r := newResScene(t)
		r.feed(t, r.host, r.w.now.Add(-11*time.Minute), r.w.now, memSample(90*mib, 512*mib))
		r.sweepAt(t, r.w.now)
		if _, ok := r.problem(t, r.subject("mem")); !ok {
			t.Fatal("floor did not open")
		}
		r = newResScene(t)
		r.settings(t, store.HostAlertSettings{AlertsEnabled: true, MemMinAvailablePercent: intp(15)})
		r.feed(t, r.host, r.w.now.Add(-11*time.Minute), r.w.now, memSample(90*mib, 512*mib))
		r.sweepAt(t, r.w.now)
		if _, ok := r.problem(t, r.subject("mem")); ok {
			t.Fatal("floor applied with an override")
		}
		// With the override, 110 of 512 MiB (21.5 %) resolves although it is
		// under the 128 MiB resolve floor.
		r.feed(t, r.host, r.w.now.Add(-11*time.Minute), r.w.now, memSample(50*mib, 512*mib))
		r.sweepAt(t, r.w.now)
		t1 := r.w.now.Add(6 * time.Minute)
		r.feed(t, r.host, r.w.now.Add(10*time.Second), t1, memSample(110*mib, 512*mib))
		r.sweepAt(t, t1)
		if p, ok := r.problem(t, r.subject("mem")); !ok || p.ResolvedAt == nil {
			t.Fatalf("override resolve %+v %v", p, ok)
		}
	})
	t.Run("disk override 90 resolves under 85", func(t *testing.T) {
		r := newResScene(t)
		r.settings(t, store.HostAlertSettings{AlertsEnabled: true, DiskMaxUsedPercent: intp(90)})
		t0 := r.w.now
		r.feed(t, r.host, t0.Add(-11*time.Minute), t0, diskSample("/", 91))
		r.sweepAt(t, t0)
		t1 := t0.Add(6 * time.Minute)
		r.feed(t, r.host, t0.Add(10*time.Second), t1, diskSample("/", 86))
		r.sweepAt(t, t1)
		if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt != nil {
			t.Fatal("resolved at 86")
		}
		t2 := t1.Add(6 * time.Minute)
		r.feed(t, r.host, t1.Add(10*time.Second), t2, diskSample("/", 84))
		r.sweepAt(t, t2)
		if p, _ := r.problem(t, r.subject("disk:/")); p.ResolvedAt == nil || p.Detail != "Below 85 % for 5 min." {
			t.Fatalf("not resolved at 84: %+v", p)
		}
	})
}

// TestHostResourceMountSafety (review items 1, 2 and 4): an unprintable or
// very long mount gets a hashed subject and a clean label, a printable one
// stays in the subject and the link; at most 50 mounts per host are judged.
func TestHostResourceMountSafety(t *testing.T) {
	r := newResScene(t)
	t0 := r.w.now
	odd := "/mnt/a\x07b"
	long := "/" + strings.Repeat("x", 300)
	for _, path := range []string{"/srv data", odd, long} {
		r.feed(t, r.host, t0.Add(-11*time.Minute), t0, diskSample(path, 95))
	}
	r.sweepAt(t, t0)
	byTitle := map[string]store.Problem{}
	for _, p := range allProblems(t, r.w) {
		if !store.ValidProblemOpen(store.ProblemOpen{Kind: p.Kind, Subject: p.Subject, OrgID: p.OrgID, Level: p.Level, Title: p.Title, Detail: p.Detail}) {
			t.Fatalf("invalid problem %+v", p)
		}
		if strings.ContainsAny(p.Title, "\x07") {
			t.Fatalf("control character in %q", p.Title)
		}
		byTitle[p.Subject] = p
	}
	if len(byTitle) != 3 {
		t.Fatalf("problems %+v", byTitle)
	}
	plain, ok := byTitle[r.subject("disk:/srv data")]
	if !ok || plain.Title != "pi1 disk /srv data is 95 % full" {
		t.Fatalf("plain %+v", byTitle)
	}
	if _, ok := byTitle[r.subject("disk:"+pathHash(odd))]; !ok {
		t.Fatal("control character path not hashed")
	}
	lp, ok := byTitle[r.subject("disk:"+pathHash(long))]
	if !ok || !strings.HasSuffix(lp.Title[:len(lp.Title)-len(" is 95 % full")], "…") {
		t.Fatalf("long path %+v", lp)
	}
	// Links: the plain mount and the hashed long one both carry the path.
	org := "/v1/orgs/" + r.p.OrgID.String() + "/problems"
	links := map[string]bool{}
	for _, it := range getProblems(t, r.w, r.w.token(r.owner.ID), org) {
		links[it.Href] = true
	}
	base := "/hosts/" + r.host.ID.String() + "?range=1h&disk="
	for _, want := range []string{base + "%2Fsrv+data", base + url.QueryEscape(long), base + url.QueryEscape(odd)} {
		if !links[want] {
			t.Fatalf("missing link %s in %v", want, links)
		}
	}

	// 60 full mounts: 50 are judged, by path order.
	r2 := newResScene(t)
	for i := range 60 {
		r2.feed(t, r2.host, t0.Add(-11*time.Minute), t0, diskSample(fmt.Sprintf("/m%02d", i), 95))
	}
	r2.sweepAt(t, t0)
	if n := len(allProblems(t, r2.w)); n != 50 {
		t.Fatalf("judged %d mounts", n)
	}
	if _, ok := r2.problem(t, r2.subject("disk:/m50")); ok {
		t.Fatal("mount past the cap judged")
	}
}
