package agentws

import (
	"context"
	"encoding/json"
	"errors"
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
	if host.RevokedAt != nil || h.Status(host, h.now()) != "online" {
		return LiveResult{}, &CallError{Code: "unavailable", Msg: "host unavailable"}
	}
	if spec == nil {
		spec = map[string]any{}
	}
	taskID := uuid.New()
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
	h.waiters[taskID] = waiter{conn: ac, ch: ch}
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
	switch f.State {
	case "succeeded":
		out.res = LiveResult{TaskID: id, Payload: taskPayload(f.Payload)}
	case "timed_out":
		out.err = &CallError{Code: "timeout", Msg: "timed out"}
	case "failed":
		out.err = failedTask(jsonString(f.ErrorCode), taskPayload(f.Payload))
	default:
		out.err = &CallError{Code: "error", Msg: jsonString(f.ErrorCode), Payload: taskPayload(f.Payload)}
	}
	h.deliver(id, out)
	return ac.write(map[string]any{
		"v":       1,
		"type":    "task_ack",
		"task_id": id.String(),
	})
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
	if len(f.Payload) > 0 {
		// Only the edge reads it (its refusal detail).
		ce.Payload = taskPayload(f.Payload)
	}
	h.deliver(id, taskOut{err: ce})
	_ = ac
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

func (h *Hub) deliver(id uuid.UUID, out taskOut) {
	h.mu.Lock()
	w, ok := h.waiters[id]
	if ok {
		delete(h.waiters, id)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	w.ch <- out
}
