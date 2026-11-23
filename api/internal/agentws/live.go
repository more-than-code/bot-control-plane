package agentws

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/ingest"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// CallError is a live-task failure. Code is an API envelope code.
// Payload is set when the agent sent a failed result body.
type CallError struct {
	Code    string
	Msg     string
	Payload json.RawMessage
	// Agent is the agent's own error_code (a task_reject or a failed
	// result), "" for a timeout or an unavailable host. The edge maps it
	// to a sentence (api-v1 v1.35).
	Agent string
	// MaybeSent marks an unavailable answer given after the task frame
	// may already have reached the host (the socket dropped or was
	// replaced while waiting). The task may have run (api-v1 v1.47).
	MaybeSent bool
	// FromResult marks an error built from the agent's own task_result
	// (failed or timed_out), which a v1.21 agent keeps until
	// task_result_ack (api-v1 v1.50 item 9).
	FromResult bool
	// Field is the key a task_reject bad_spec names (agent-protocol
	// v1.23), checked by RejectField; "" when the agent sent none.
	Field string
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Msg
}

// LiveResult is a succeeded task payload. Payload is the agent JSON value, or null.
type LiveResult struct {
	TaskID  uuid.UUID
	Payload json.RawMessage
}

// Snapshot asks a live host for a host_snapshot.
func (h *Hub) Snapshot(ctx context.Context, host store.Host) (LiveResult, error) {
	return h.live(ctx, host, "host_snapshot", nil, map[string]any{}, taskTimeoutSec)
}

// Program asks a live host to run a v1.3 program task. service is null.
// Program frames stay at taskTimeoutSec and taskTTL.
func (h *Hub) Program(ctx context.Context, host store.Host, kind string, spec map[string]any) (LiveResult, error) {
	return h.live(ctx, host, kind, nil, spec, taskTimeoutSec)
}

// Image asks a live host to run a v1.4 image task. service is null.
// timeoutSec is the task frame timeout. Above 60s it is also the frame TTL.
func (h *Hub) Image(ctx context.Context, host store.Host, kind string, spec map[string]any, timeoutSec int) (LiveResult, error) {
	if timeoutSec < 1 {
		timeoutSec = taskTimeoutSec
	}
	return h.live(ctx, host, kind, nil, spec, timeoutSec)
}

// ImageWithID is Image with a task id the caller chose, so it can record
// the id before the frame is sent (api-v1 v1.50 item 3).
func (h *Hub) ImageWithID(ctx context.Context, host store.Host, kind string, spec map[string]any, timeoutSec int, taskID uuid.UUID) (LiveResult, error) {
	if timeoutSec < 1 {
		timeoutSec = taskTimeoutSec
	}
	return h.liveID(ctx, host, kind, nil, spec, timeoutSec, taskID)
}

// EdgeTimeoutSec is the edge task budget: the first apply may pull the
// Caddy image (agent-protocol v1.15 item 4).
const EdgeTimeoutSec = 180

// Edge asks a live host to run edge_apply, edge_stop or edge_status
// (agent-protocol v1.15 item 4). service is null.
func (h *Hub) Edge(ctx context.Context, host store.Host, kind string, spec map[string]any) (LiveResult, error) {
	return h.live(ctx, host, kind, nil, spec, EdgeTimeoutSec)
}

// SetEdgeHook sets the function that re-applies a host's edge config. It is
// called in its own goroutine after each hello (hello true) and services
// report (hello false).
func (h *Hub) SetEdgeHook(fn func(hostID uuid.UUID, hello bool)) {
	h.mu.Lock()
	h.edgeHook = fn
	h.mu.Unlock()
}

// DeployHook is called after a stored agent auto action, with the entry's
// project, id and name and the host.
type DeployHook func(ctx context.Context, projectID, entryID uuid.UUID, entry string, hostID uuid.UUID)

// SetDeployHook sets the function that evaluates the deploy problem after
// an auto action is stored (api-v1 v1.41). It runs in its own goroutine so
// a slow store never holds up the agent's read loop.
func (h *Hub) SetDeployHook(fn DeployHook) {
	h.mu.Lock()
	h.deployHook = fn
	h.mu.Unlock()
}

func (h *Hub) notifyDeploy(ctx context.Context, projectID, entryID uuid.UUID, entry string, hostID uuid.UUID) {
	h.mu.Lock()
	fn := h.deployHook
	h.mu.Unlock()
	if fn == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	h.hooks.Add(1)
	go func() {
		defer h.hooks.Done()
		fn(ctx, projectID, entryID, entry, hostID)
	}()
}

// WaitHooks waits for the deploy hook calls already started (tests).
func (h *Hub) WaitHooks() { h.hooks.Wait() }

func (h *Hub) notifyEdge(hostID uuid.UUID, hello bool) {
	h.mu.Lock()
	fn := h.edgeHook
	h.mu.Unlock()
	if fn != nil {
		go fn(hostID, hello)
	}
}

// Probe asks a live host to run probe_health for svc.
func (h *Hub) Probe(ctx context.Context, host store.Host, svc store.Service, spec map[string]any) (LiveResult, error) {
	name := svc.Name
	if spec == nil {
		spec = map[string]any{}
	}
	return h.live(ctx, host, "probe_health", &name, spec, taskTimeoutSec)
}

func (h *Hub) live(ctx context.Context, host store.Host, kind string, service *string, spec map[string]any, timeoutSec int) (LiveResult, error) {
	return h.liveID(ctx, host, kind, service, spec, timeoutSec, uuid.New())
}

func (h *Hub) liveID(ctx context.Context, host store.Host, kind string, service *string, spec map[string]any, timeoutSec int, taskID uuid.UUID) (LiveResult, error) {
	if host.RevokedAt != nil || h.Status(host, h.now()) != "online" {
		return LiveResult{}, &CallError{Code: "unavailable", Msg: "host unavailable"}
	}
	if spec == nil {
		spec = map[string]any{}
	}
	now := h.now()
	ttl := taskTTL
	if timeoutSec > int(taskTTL/time.Second) {
		ttl = time.Duration(timeoutSec) * time.Second
	}
	frame := taskFrame{
		V:          1,
		Type:       "task",
		TaskID:     taskID,
		HostID:     host.ID,
		Kind:       kind,
		Service:    service,
		ExpiresAt:  now.Add(ttl).UTC().Format(time.RFC3339),
		TimeoutSec: timeoutSec,
		Spec:       spec,
	}
	ch := make(chan taskOut, 1)
	h.mu.Lock()
	ac := h.conns[host.ID]
	online := ac != nil && h.now().Sub(ac.lastPing) <= offlineAfter
	if !online || host.RevokedAt != nil {
		h.mu.Unlock()
		return LiveResult{}, &CallError{Code: "unavailable", Msg: "host unavailable"}
	}
	h.waiters[taskID] = waiter{conn: ac, ch: ch, kind: kind}
	h.mu.Unlock()

	if err := ac.writeJSON(ctx, frame); err != nil {
		h.dropWaiter(taskID)
		// The API built this frame. A size refusal writes nothing and must not drop the socket.
		if errors.Is(err, errFrameTooLarge) {
			msg := "bad request"
			if kind == "image_deploy" || kind == "image_rollback" {
				msg = EnvFrameTooLarge
			}
			return LiveResult{}, &CallError{Code: "bad_request", Msg: msg}
		}
		go ac.close(websocket.StatusInternalError, "")
		return LiveResult{}, &CallError{Code: "unavailable", Msg: "host unavailable"}
	}

	select {
	case <-ctx.Done():
		if out, ok := h.takeWaiter(taskID, ch); ok {
			return finishTask(taskID, out)
		}
		return LiveResult{}, &CallError{Code: "timeout", Msg: "timed out"}
	case out := <-ch:
		return finishTask(taskID, out)
	}
}

func finishTask(taskID uuid.UUID, out taskOut) (LiveResult, error) {
	if out.err != nil {
		return LiveResult{}, out.err
	}
	if out.res.TaskID == uuid.Nil {
		out.res.TaskID = taskID
	}
	if len(out.res.Payload) == 0 {
		out.res.Payload = json.RawMessage("null")
	}
	return out.res, nil
}

func (h *Hub) dropWaiter(id uuid.UUID) {
	h.mu.Lock()
	delete(h.waiters, id)
	h.mu.Unlock()
}

func (h *Hub) takeWaiter(id uuid.UUID, ch chan taskOut) (taskOut, bool) {
	h.mu.Lock()
	_, pending := h.waiters[id]
	if pending {
		delete(h.waiters, id)
	}
	h.mu.Unlock()
	if !pending {
		select {
		case out := <-ch:
			return out, true
		default:
			return taskOut{}, false
		}
	}
	select {
	case out := <-ch:
		return out, true
	default:
		return taskOut{}, false
	}
}

func (h *Hub) onTaskResult(ac *agentConn, f frame) error {
	id, err := uuid.Parse(f.TaskID)
	if err != nil {
		h.log.Warn("bad task_id")
		return nil
	}
	var out taskOut
	var ce *CallError
	switch f.State {
	case "succeeded":
		out.res = LiveResult{TaskID: id, Payload: taskPayload(f.Payload)}
	case "timed_out":
		ce = &CallError{Code: "timeout", Msg: "timed out"}
	case "failed":
		ce = failedTask(jsonString(f.ErrorCode), taskPayload(f.Payload))
	default:
		ce = &CallError{Code: "error", Msg: jsonString(f.ErrorCode), Payload: taskPayload(f.Payload)}
	}
	if ce != nil {
		ce.FromResult = true
		out.err = ce
	}
	var ackResult bool
	switch kind, got := h.deliver(id, out, ac); got {
	case delivered:
		// An image deploy or rollback is acked by the API once its row is
		// written (AckResult, api-v1 v1.50 item 9); a run has no row.
		ackResult = kind == "image_start"
	case foreignConn:
		// A waiter exists, but for another connection: not this socket's
		// task. Acked and dropped (api-v1 v1.50 item 9).
		ac.log.Info("task result from another connection dropped", "task_id", id.String())
		ackResult = true
	default:
		ackResult = h.lateResult(ac, id, out)
	}
	if err := ac.write(map[string]any{
		"v":       1,
		"type":    "task_ack",
		"task_id": id.String(),
	}); err != nil {
		return err
	}
	if !ackResult {
		return nil
	}
	return writeResultAck(ac, id)
}

// writeResultAck sends task_result_ack (agent-protocol v1.21 item 5): the
// agent deletes its stored result on it. An older agent ignores the type.
func writeResultAck(ac *agentConn, id uuid.UUID) error {
	return ac.write(map[string]any{
		"v":       1,
		"type":    "task_result_ack",
		"task_id": id.String(),
	})
}

// AckResult sends task_result_ack for taskID to the host's current socket,
// after the API stored the result (api-v1 v1.50 item 9). Without a socket
// it does nothing: the agent resends the result after its next hello, and
// the late path acks it.
func (h *Hub) AckResult(hostID, taskID uuid.UUID) {
	h.mu.Lock()
	ac := h.conns[hostID]
	h.mu.Unlock()
	if ac == nil {
		return
	}
	if err := writeResultAck(ac, taskID); err != nil {
		ac.log.Warn("task result ack not sent", "task_id", taskID.String())
	}
}

// LateHook settles the deploy or rollback row whose task is taskID on
// hostID from a result no waiter in this process took (api-v1 v1.50 item
// 3): the waiting request ended, timed out, or ran in another API process
// or before a restart. The frame's own "late" flag is not needed: a result
// no waiter took is late here either way. A row that does not
// exist or is already settled is not an error. A non-nil error means the
// store failed: the result is not acked, so the agent resends it after its
// next hello.
type LateHook func(ctx context.Context, hostID, taskID uuid.UUID, res LiveResult, err error) error

// SetLateHook sets the function that settles late deploy results. It runs
// on the read loop.
func (h *Hub) SetLateHook(fn LateHook) {
	h.mu.Lock()
	h.lateHook = fn
	h.mu.Unlock()
}

// lateResult hands a result no waiter took to the late hook and reports
// whether to send task_result_ack. Without a hook the result is dropped
// and acked, logged by task id only.
func (h *Hub) lateResult(ac *agentConn, id uuid.UUID, out taskOut) bool {
	h.mu.Lock()
	fn := h.lateHook
	h.mu.Unlock()
	if fn == nil {
		ac.log.Info("task result with no waiter dropped", "task_id", id.String())
		return true
	}
	res, callErr := finishTask(id, out)
	if err := fn(ac.parent, ac.hostID, id, res, callErr); err != nil {
		ac.log.Error("late task result not stored", "task_id", id.String())
		return false
	}
	return true
}

func (h *Hub) onTaskReject(ac *agentConn, f frame) {
	id, err := uuid.Parse(f.TaskID)
	if err != nil {
		h.log.Warn("bad task_id")
		return
	}
	agent := jsonString(f.ErrorCode)
	code, msg := rejectCode(agent)
	ce := &CallError{Code: code, Msg: msg, Agent: agent}
	if agent == "bad_spec" {
		ce.Field = RejectField(jsonString(f.Field))
	}
	if len(f.Payload) > 0 {
		// Only the edge reads it (its refusal detail).
		ce.Payload = taskPayload(f.Payload)
	}
	h.deliver(id, taskOut{err: ce}, ac)
}

// rejectFieldRe is the agent-protocol v1.23 task_reject field shape: up to
// three dot-separated key names, never a value.
var rejectFieldRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31}){0,2}$`)

// RejectField returns s when it is a valid task_reject field, else "".
func RejectField(s string) string {
	if len(s) > 64 || !rejectFieldRe.MatchString(s) {
		return ""
	}
	return s
}

func rejectCode(code string) (string, string) {
	switch code {
	case "forbidden":
		return "forbidden", "forbidden"
	case "unknown_service":
		return "not_found", "not found"
	case "bad_spec":
		return "bad_request", "bad request"
	case "busy":
		return "unavailable", "host unavailable"
	case "policy_denied":
		return "forbidden", PolicyDenied
	case "edge_disabled":
		return "forbidden", EdgeDisabled
	case "port_in_use":
		return "conflict", EdgePortInUse
	case "invalid_config":
		return "bad_request", EdgeInvalidConfig
	default:
		return "error", "internal error"
	}
}

// PolicyDenied is the message for an agent policy_denied (api-v1 v1.23 item
// 3). The agent's own reason is never part of it.
const PolicyDenied = "the host refused this under its policy (runner.toml)"

// Edge refusals (agent-protocol v1.15 item 4). A failed result keeps the
// agent payload (the holder's name, Caddy's first error line).
const (
	EdgeDisabled      = "the host's runner.toml does not allow the edge"
	EdgePortInUse     = "port 80 or 443 is in use on the host"
	EdgeInvalidConfig = "the edge refused the config"
)

func failedTask(code string, payload json.RawMessage) *CallError {
	if code == "policy_denied" {
		return &CallError{Code: "forbidden", Msg: PolicyDenied, Agent: code}
	}
	switch code {
	case "edge_disabled", "port_in_use", "invalid_config":
		c, msg := rejectCode(code)
		return &CallError{Code: c, Msg: msg, Payload: payload, Agent: code}
	}
	return &CallError{Code: "error", Msg: code, Payload: payload, Agent: code}
}

func taskPayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return ingest.RedactPayload(raw)
}

// Delivery outcomes.
const (
	noWaiter = iota
	delivered
	foreignConn
)

// deliver hands out to the task's waiter when the waiter's task was sent on
// from (the delivering socket), and reports the task kind and the outcome.
// A waiter of another socket is left waiting.
func (h *Hub) deliver(id uuid.UUID, out taskOut, from *agentConn) (string, int) {
	h.mu.Lock()
	w, ok := h.waiters[id]
	if ok && w.conn != from {
		h.mu.Unlock()
		return w.kind, foreignConn
	}
	if ok {
		delete(h.waiters, id)
	}
	h.mu.Unlock()
	if !ok {
		return "", noWaiter
	}
	w.ch <- out
	return w.kind, delivered
}
