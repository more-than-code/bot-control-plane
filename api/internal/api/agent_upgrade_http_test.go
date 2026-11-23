package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.48 agent upgrades from the console or MCP.

const upgradeLatest = "0.2.15"

func upgradePrograms(allowed bool) map[string]any {
	p := allowPrograms(false)
	if allowed {
		p["agent_upgrade"] = true
	}
	return p
}

// newUpgradeCast is a host on agent 0.2.12 with the latest version 0.2.15,
// connected with programs unless programs is nil. The upgrade hook is set.
func newUpgradeCast(t *testing.T, programs map[string]any) *hfCast {
	t.Helper()
	c := newHFCast(t, nil)
	c.w.srv.LatestAgentVersion = upgradeLatest
	c.w.hub.SetUpgradeHook(c.w.srv.UpgradeHook)
	c.base = "/v1/hosts/" + c.sc.host.ID.String()
	if programs != nil {
		c.connect(t, programs)
	}
	return c
}

func accepted() agentReply {
	return succeed(`{"state":"accepted"}`)
}

func (c *hfCast) upgrade(t *testing.T, tok string, want int, code string) envelope {
	t.Helper()
	return c.do(t, tok, "POST", "/agent-upgrade", `{"version":"`+upgradeLatest+`"}`, want, code)
}

func (c *hfCast) rows(t *testing.T) []store.AgentUpgrade {
	t.Helper()
	rows, err := c.w.mem.ListAgentUpgrades(context.Background(), c.sc.host.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (c *hfCast) upgradeProblem(t *testing.T) *store.Problem {
	t.Helper()
	since := time.Time{}
	rows, err := c.w.mem.ListProblems(context.Background(), store.ProblemFilter{All: true, Kind: store.ProblemAgentUpgrade, ResolvedSince: &since})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].Subject == c.sc.host.ID.String() {
			return &rows[i]
		}
	}
	return nil
}

// rehello reconnects the agent with agent_version and last_upgrade; the
// new socket answers tasks like the first.
func (c *hfCast) rehello(t *testing.T, version string, last any) {
	t.Helper()
	c.connectHello(t, map[string]any{"agent_version": version, "programs": upgradePrograms(true), "last_upgrade": last})
}

func lastUpgrade(version, outcome, reason string, at time.Time) map[string]any {
	return map[string]any{"version": version, "outcome": outcome, "reason": reason, "at": at.UTC().Format(time.RFC3339)}
}

func TestAgentUpgradeAuthz(t *testing.T) {
	c := newUpgradeCast(t, upgradePrograms(true))
	c.setReply(accepted())
	// No relation is not_found; an org member or a project admin outside the org is forbidden.
	c.upgrade(t, c.extraTok, http.StatusNotFound, "not_found")
	c.upgrade(t, c.memberTok, http.StatusNotFound, "not_found")
	c.upgrade(t, c.projTok, http.StatusForbidden, "forbidden")
	c.upgrade(t, c.viewTok, http.StatusForbidden, "forbidden")
	if len(c.rows(t)) != 0 || len(c.taskSpecs()) != 0 {
		t.Fatal("a refused caller started an upgrade")
	}
	denied := 0
	for _, a := range auditsOf(c, audit.ActionHostAgentUpgrade) {
		if a.Decision == audit.DecisionDenied {
			denied++
		}
	}
	if denied < 2 {
		t.Fatalf("denied audits %d", denied)
	}
	// The org admin, over an MCP-delegated session.
	delegated := tokenKind(t, c.w, mustUser(t, c, "orgadmin@ex.com"), store.KindDelegated)
	env := c.upgrade(t, delegated, http.StatusAccepted, "ok")
	p := objectPayload(t, env)
	if p["state"] != "running" || p["upgrade_id"] == "" {
		t.Fatalf("payload %#v", p)
	}
	specs := c.taskSpecs()
	if len(specs) != 1 || len(specs[0]) != 1 || specs[0]["version"] != upgradeLatest {
		t.Fatalf("task specs %#v", specs)
	}
	rows := c.rows(t)
	if len(rows) != 1 || rows[0].State != store.AgentUpgradeRunning || rows[0].From != "0.2.12" || rows[0].To != upgradeLatest {
		t.Fatalf("rows %+v", rows)
	}
	starts := auditsOf(c, audit.ActionHostAgentUpgrade)
	last := starts[len(starts)-1]
	if last.Decision != audit.DecisionAllowed || last.Via != audit.ViaMCP || strings.Join(detailKeys(t, last), ",") != "from,host,to" {
		t.Fatalf("start audit %+v %v", last, detailKeys(t, last))
	}
	// A second click while it runs is conflict.
	c.upgrade(t, c.ownerTok, http.StatusConflict, "conflict")
}

func mustUser(t *testing.T, c *hfCast, email string) uuid.UUID {
	t.Helper()
	u, err := c.w.mem.UserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestAgentUpgradeRefusalOrder(t *testing.T) {
	t.Run("version before opt-in", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(false))
		c.do(t, c.ownerTok, "POST", "/agent-upgrade", `{"version":"0.2.14"}`, http.StatusBadRequest, "bad_request")
		c.do(t, c.ownerTok, "POST", "/agent-upgrade", `{}`, http.StatusBadRequest, "bad_request")
		c.upgrade(t, c.ownerTok, http.StatusConflict, "unsupported_agent")
	})
	t.Run("not older", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.w.srv.LatestAgentVersion = "0.2.12"
		c.do(t, c.ownerTok, "POST", "/agent-upgrade", `{"version":"0.2.12"}`, http.StatusBadRequest, "bad_request")
	})
	t.Run("bad latest setting", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.w.srv.LatestAgentVersion = "12345.0.0"
		c.do(t, c.ownerTok, "POST", "/agent-upgrade", `{"version":"12345.0.0"}`, http.StatusBadRequest, "bad_request")
		if len(c.rows(t)) != 0 {
			t.Fatal("a row was inserted")
		}
	})
	t.Run("opt-in before offline", func(t *testing.T) {
		c := newUpgradeCast(t, nil)
		setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}})
		env := c.upgrade(t, c.ownerTok, http.StatusConflict, "unsupported_agent")
		if env.Msg != msgUpgradeNotAllowed {
			t.Fatalf("msg %q", env.Msg)
		}
		setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, AgentUpgrade: true})
		c.upgrade(t, c.ownerTok, http.StatusServiceUnavailable, "host_offline")
	})
	t.Run("revoked first", func(t *testing.T) {
		c := newUpgradeCast(t, nil)
		if err := c.w.mem.RevokeHost(context.Background(), c.sc.host.ID, c.w.now); err != nil {
			t.Fatal(err)
		}
		c.do(t, c.ownerTok, "POST", "/agent-upgrade", `{"version":"0.0.1"}`, http.StatusConflict, "conflict")
	})
	t.Run("running last", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		if _, err := c.w.mem.StartAgentUpgrade(context.Background(), store.AgentUpgrade{
			ID: uuid.New(), HostID: c.sc.host.ID, From: "0.2.12", To: upgradeLatest, StartedBy: c.owner.ID, StartedAt: c.w.now,
		}); err != nil {
			t.Fatal(err)
		}
		env := c.upgrade(t, c.ownerTok, http.StatusConflict, "conflict")
		if env.Msg != msgUpgradeRunning || len(c.taskSpecs()) != 0 {
			t.Fatalf("running %q %d", env.Msg, len(c.taskSpecs()))
		}
	})
}

func TestAgentUpgradeTaskOutcomes(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.setReply(func(map[string]any) (map[string]any, time.Duration) {
			return map[string]any{"type": "task_result", "state": "failed", "error_code": "busy"}, 0
		})
		env := c.upgrade(t, c.ownerTok, http.StatusConflict, "conflict")
		if objectPayload(t, env)["reason"] != "busy" {
			t.Fatalf("payload %#v", env.Payload)
		}
		rows := c.rows(t)
		if len(rows) != 1 || rows[0].State != store.AgentUpgradeFailed || rows[0].Reason != "busy" || rows[0].FinishedAt == nil {
			t.Fatalf("rows %+v", rows)
		}
		// A synchronous refusal opens no problem: the caller saw the 409 (review M1 c).
		if p := c.upgradeProblem(t); p != nil {
			t.Fatalf("problem %+v", p)
		}
		res := auditsOf(c, audit.ActionHostAgentUpgradeResult)
		if len(res) != 1 || strings.Join(detailKeys(t, res[0]), ",") != "host,reason,state,to" {
			t.Fatalf("result audit %+v", res)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.setReply(func(map[string]any) (map[string]any, time.Duration) {
			return map[string]any{"type": "task_reject", "error_code": "forbidden"}, 0
		})
		env := c.upgrade(t, c.ownerTok, http.StatusConflict, "conflict")
		if objectPayload(t, env)["reason"] != "forbidden" || c.rows(t)[0].Reason != "forbidden" || c.upgradeProblem(t) != nil {
			t.Fatalf("payload %#v", env.Payload)
		}
	})
	t.Run("transport", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.setReply(func(map[string]any) (map[string]any, time.Duration) {
			go c.conn.Close(websocket.StatusNormalClosure, "")
			return nil, 0
		})
		c.upgrade(t, c.ownerTok, http.StatusServiceUnavailable, "unavailable")
		// The helper may have started the unit: the row stays open, with no
		// result audit and no problem.
		rows := c.rows(t)
		if len(rows) != 1 || rows[0].State != store.AgentUpgradeStarted || rows[0].FinishedAt != nil {
			t.Fatalf("rows %+v", rows)
		}
		if len(auditsOf(c, audit.ActionHostAgentUpgradeResult)) != 0 || c.upgradeProblem(t) != nil {
			t.Fatal("an unanswered task settled the row")
		}
		// A second click is refused while it is open.
		c.rehello(t, "0.2.12", nil)
		c.upgrade(t, c.ownerTok, http.StatusConflict, "conflict")
		// The host's later hello with the result settles it.
		c.rehello(t, upgradeLatest, lastUpgrade(upgradeLatest, "succeeded", "", c.w.now.Add(time.Minute)))
		hfWait(t, "succeeded", func() bool { return c.rows(t)[0].State == store.AgentUpgradeSucceeded })
	})
	t.Run("timeout then rolled back", func(t *testing.T) {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.w.srv.agentUpgradeWaitTest = 50 * time.Millisecond
		c.setReply(func(map[string]any) (map[string]any, time.Duration) { return nil, 0 })
		c.upgrade(t, c.ownerTok, http.StatusServiceUnavailable, "unavailable")
		if r := c.rows(t)[0]; r.State != store.AgentUpgradeStarted {
			t.Fatalf("row %+v", r)
		}
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(time.Minute)))
		hfWait(t, "rolled_back", func() bool { return c.rows(t)[0].State == store.AgentUpgradeRolledBack })
		if r := c.rows(t)[0]; r.Reason != "not_reconnected" {
			t.Fatalf("row %+v", r)
		}
	})
}

func TestAgentUpgradeSettle(t *testing.T) {
	start := func(t *testing.T) *hfCast {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.setReply(accepted())
		c.upgrade(t, c.ownerTok, http.StatusAccepted, "ok")
		return c
	}
	state := func(c *hfCast) string { return c.rows(c.w.t)[0].State }
	t.Run("hello version", func(t *testing.T) {
		c := start(t)
		// A result for another version and an older agent leave the row running.
		c.rehello(t, "0.2.12", lastUpgrade("0.2.14", "rolled_back", "not_reconnected", c.w.now))
		if state(c) != store.AgentUpgradeRunning {
			t.Fatalf("state %s", state(c))
		}
		// A result for the target from before the row started is an old one.
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(-time.Hour)))
		if state(c) != store.AgentUpgradeRunning {
			t.Fatalf("state %s", state(c))
		}
		c.rehello(t, upgradeLatest, nil)
		hfWait(t, "succeeded", func() bool { return state(c) == store.AgentUpgradeSucceeded })
	})
	t.Run("hello result", func(t *testing.T) {
		c := start(t)
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(time.Minute)))
		hfWait(t, "rolled_back", func() bool { p := c.upgradeProblem(t); return p != nil && p.ResolvedAt == nil })
		rows := c.rows(t)
		if rows[0].State != store.AgentUpgradeRolledBack || rows[0].Reason != "not_reconnected" || rows[0].FinishedAt == nil {
			t.Fatalf("rows %+v", rows)
		}
		if p := c.upgradeProblem(t); p == nil || p.ResolvedAt != nil {
			t.Fatalf("problem %+v", p)
		}
		// The next upgrade that succeeds resolves it.
		c.rehello(t, "0.2.12", nil)
		later := c.w.now.Add(30 * time.Second)
		c.w.srv.Now = func() time.Time { return later }
		c.upgrade(t, c.ownerTok, http.StatusAccepted, "ok")
		c.rehello(t, upgradeLatest, lastUpgrade(upgradeLatest, "succeeded", "", later.Add(time.Minute)))
		hfWait(t, "succeeded", func() bool { return c.rows(t)[0].State == store.AgentUpgradeSucceeded })
		if p := c.upgradeProblem(t); p == nil || p.ResolvedAt == nil {
			t.Fatalf("problem %+v rows %+v", p, c.rows(t))
		}
		res := auditsOf(c, audit.ActionHostAgentUpgradeResult)
		if len(res) != 2 || res[0].Via != audit.ViaAgent {
			t.Fatalf("result audits %+v", res)
		}
	})
	t.Run("frame", func(t *testing.T) {
		c := start(t)
		writeFrame(t, c.conn, map[string]any{"v": 1, "type": "agent_upgrade_result", "result": lastUpgrade(upgradeLatest, "failed", "signature_invalid", c.w.now)})
		hfWait(t, "failed", func() bool { return state(c) == store.AgentUpgradeFailed })
		if r := c.rows(t)[0]; r.Reason != "signature_invalid" {
			t.Fatalf("row %+v", r)
		}
		// A failure the host reported opens the problem; the bell shows it to
		// org owners and admins, linked to the host page.
		if p := c.upgradeProblem(t); p == nil || p.ResolvedAt != nil || p.Level != store.ProblemNeedsAction {
			t.Fatalf("problem %+v", p)
		}
		_, bell := c.w.do(c.ownerTok, "GET", "/v1/me/notifications", "")
		if j := envJSON(bell); !strings.Contains(j, "upgrade on pi4 failed") || !strings.Contains(j, `"/hosts/`+c.sc.host.ID.String()+`"`) {
			t.Fatalf("owner bell %s", j)
		}
		if _, bell := c.w.do(c.viewTok, "GET", "/v1/me/notifications", ""); strings.Contains(envJSON(bell), "upgrade on pi4") {
			t.Fatalf("viewer bell %s", envJSON(bell))
		}
	})
	t.Run("malformed frame", func(t *testing.T) {
		c := start(t)
		// A malformed frame keeps the socket and the row.
		writeFrame(t, c.conn, map[string]any{"v": 1, "type": "agent_upgrade_result", "result": "nope"})
		writeFrame(t, c.conn, map[string]any{"v": 1, "type": "ping"})
		hfWait(t, "malformed frame logged", func() bool { return strings.Contains(c.logs.String(), "bad agent_upgrade_result") })
		time.Sleep(50 * time.Millisecond)
		host, _ := c.w.mem.GetHost(context.Background(), c.sc.host.ID)
		if c.w.hub.Status(host, c.w.now) != "online" || state(c) != store.AgentUpgradeRunning {
			t.Fatal("socket dropped or row settled")
		}
	})
	t.Run("unknown after 15 minutes", func(t *testing.T) {
		c := start(t)
		later := c.w.now.Add(14 * time.Minute)
		c.w.srv.Now = func() time.Time { return later }
		c.w.srv.SweepAgentUpgrades(context.Background())
		if state(c) != store.AgentUpgradeRunning {
			t.Fatalf("state %s", state(c))
		}
		later = c.w.now.Add(16 * time.Minute)
		c.w.srv.SweepAgentUpgrades(context.Background())
		if state(c) != store.AgentUpgradeUnknown {
			t.Fatalf("state %s", state(c))
		}
		if p := c.upgradeProblem(t); p == nil || p.ResolvedAt != nil {
			t.Fatalf("problem %+v", p)
		}
	})
}

func TestAgentUpgradeReads(t *testing.T) {
	c := newUpgradeCast(t, upgradePrograms(true))
	c.setReply(accepted())
	c.upgrade(t, c.ownerTok, http.StatusAccepted, "ok")
	host := objectPayload(t, c.do(t, c.viewTok, "GET", "", "", http.StatusOK, "ok"))
	if host["agent_upgrade_allowed"] != true || host["update_available"] != true {
		t.Fatalf("host %#v", host)
	}
	if programs, _ := host["programs"].(map[string]any); programs == nil || programs["agent_upgrade"] != nil {
		t.Fatalf("programs %#v", host["programs"])
	}
	last, _ := host["agent_upgrade"].(map[string]any)
	if last == nil || last["state"] != "running" || last["to"] != upgradeLatest || last["reason"] != nil {
		t.Fatalf("agent_upgrade %#v", host["agent_upgrade"])
	}
	by, _ := last["started_by"].(map[string]any)
	if by["name"] != "Owner" {
		t.Fatalf("started_by %#v", by)
	}
	// The project host list carries the same fields.
	status, env := c.w.do(c.viewTok, "GET", "/v1/projects/"+c.sc.project.ID.String()+"/hosts", "")
	list := items(t, env)
	if status != http.StatusOK || len(list) != 1 || list[0]["agent_upgrade_allowed"] != true || list[0]["agent_upgrade"] == nil {
		t.Fatalf("list %d %s", status, envJSON(env))
	}
	hist := items(t, c.do(t, c.viewTok, "GET", "/agent-upgrades", "", http.StatusOK, "ok"))
	if len(hist) != 1 || hist[0]["state"] != "running" || hist[0]["from"] != "0.2.12" {
		t.Fatalf("history %#v", hist)
	}
	c.do(t, c.extraTok, "GET", "/agent-upgrades", "", http.StatusNotFound, "not_found")
	raw, _ := json.Marshal(hist[0])
	for _, key := range []string{"id", "from", "to", "state", "reason", "started_by", "started_at", "finished_at"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("history item lacks %s: %s", key, raw)
		}
	}
	// A revoked host never offers the upgrade.
	if err := c.w.mem.RevokeHost(context.Background(), c.sc.host.ID, c.w.now); err != nil {
		t.Fatal(err)
	}
	host = objectPayload(t, c.do(t, c.ownerTok, "GET", "", "", http.StatusOK, "ok"))
	if host["agent_upgrade_allowed"] != false {
		t.Fatalf("revoked %#v", host["agent_upgrade_allowed"])
	}
}

// Review M1 a and b, L1, L2.
func TestAgentUpgradeCatchUp(t *testing.T) {
	start := func(t *testing.T) *hfCast {
		c := newUpgradeCast(t, upgradePrograms(true))
		c.setReply(accepted())
		c.upgrade(t, c.ownerTok, http.StatusAccepted, "ok")
		return c
	}
	state := func(c *hfCast) string { return c.rows(c.w.t)[0].State }
	open := func(c *hfCast) bool { p := c.upgradeProblem(c.w.t); return p != nil && p.ResolvedAt == nil }
	t.Run("unknown then the target hello", func(t *testing.T) {
		c := start(t)
		later := c.w.now.Add(16 * time.Minute)
		c.w.srv.Now = func() time.Time { return later }
		c.w.srv.SweepAgentUpgrades(context.Background())
		if state(c) != store.AgentUpgradeUnknown || !open(c) {
			t.Fatalf("state %s", state(c))
		}
		c.w.srv.Now = func() time.Time { return c.w.now }
		c.rehello(t, upgradeLatest, nil)
		hfWait(t, "succeeded", func() bool { return state(c) == store.AgentUpgradeSucceeded })
		hfWait(t, "resolved", func() bool { return !open(c) })
		if n := len(auditsOf(c, audit.ActionHostAgentUpgradeResult)); n != 2 {
			t.Fatalf("result audits %d", n)
		}
	})
	t.Run("rolled back, then upgraded by hand", func(t *testing.T) {
		c := start(t)
		c.w.srv.LatestAgentVersion = "0.2.16"
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(time.Minute)))
		hfWait(t, "open", func() bool { return open(c) })
		// Neither the target nor the latest: the problem stays.
		c.rehello(t, "0.2.13", nil)
		time.Sleep(50 * time.Millisecond)
		if !open(c) {
			t.Fatal("resolved by an older agent")
		}
		// At or above the latest release: resolved, the row stays rolled_back.
		c.rehello(t, "0.2.16", nil)
		hfWait(t, "resolved", func() bool { return !open(c) })
		if state(c) != store.AgentUpgradeRolledBack {
			t.Fatalf("state %s", state(c))
		}
	})
	t.Run("the sweep resolves a host that caught up", func(t *testing.T) {
		c := start(t)
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(time.Minute)))
		hfWait(t, "open", func() bool { return open(c) })
		c.w.srv.SweepAgentUpgrades(context.Background())
		if !open(c) {
			t.Fatal("resolved while still older")
		}
		// The agent version changes without a hook call (a store write only).
		if err := c.w.mem.TouchHost(context.Background(), c.sc.host.ID, c.w.now, upgradeLatest, "", ""); err != nil {
			t.Fatal(err)
		}
		c.w.srv.SweepAgentUpgrades(context.Background())
		if open(c) {
			t.Fatal("sweep left it open")
		}
	})
	t.Run("a result from the future is ignored", func(t *testing.T) {
		c := start(t)
		c.rehello(t, "0.2.12", lastUpgrade(upgradeLatest, "rolled_back", "not_reconnected", c.w.now.Add(10*time.Minute)))
		hfWait(t, "logged", func() bool { return strings.Contains(c.logs.String(), "agent upgrade result from the future ignored") })
		if state(c) != store.AgentUpgradeRunning {
			t.Fatalf("state %s", state(c))
		}
	})
	t.Run("a stale started read still settles after the route set running", func(t *testing.T) {
		c := newUpgradeCast(t, nil)
		setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, AgentUpgrade: true})
		ctx := context.Background()
		row, err := c.w.mem.StartAgentUpgrade(ctx, store.AgentUpgrade{ID: uuid.New(), HostID: c.sc.host.ID, From: "0.2.12", To: upgradeLatest, StartedBy: c.owner.ID, StartedAt: c.w.now})
		if err != nil {
			t.Fatal(err)
		}
		// The hook has read row (started); the route now moves it to running.
		if ok, _ := c.w.mem.SettleAgentUpgrade(ctx, row.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeRunning, "", c.w.now); !ok {
			t.Fatal("running")
		}
		host, _ := c.w.mem.GetHost(ctx, c.sc.host.ID)
		c.w.srv.settleUpgrade(ctx, host, row, store.AgentUpgradeOpenStates, store.AgentUpgradeSucceeded, "", audit.ViaAgent, true)
		if state(c) != store.AgentUpgradeSucceeded {
			t.Fatalf("state %s", state(c))
		}
	})
}
