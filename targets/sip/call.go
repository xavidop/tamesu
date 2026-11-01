package sip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/targets/livekit"
)

// outbound dials the agent through the trunk.
type outbound struct {
	cfg Config
	api API

	mu       sync.Mutex
	identity string
}

func (o *outbound) MaxCallDuration() time.Duration { return o.cfg.MaxCallDuration }

func (o *outbound) Place(ctx context.Context, room livekit.RoomInfo) (livekit.CallInfo, error) {
	identity := "sip-agent-" + uuid.NewString()
	o.mu.Lock()
	o.identity = identity
	o.mu.Unlock()
	req := ParticipantRequest{
		Room: room.Room, TrunkID: o.cfg.TrunkID, To: o.cfg.To, From: o.cfg.From, Identity: identity,
		RingTimeout: o.cfg.RingTimeout, MaxCallDuration: o.cfg.MaxCallDuration, Headers: o.cfg.Headers,
	}
	if err := o.api.CreateParticipant(ctx, req); err != nil {
		reason, status := callReason(err)
		return livekit.CallInfo{}, &livekit.CallError{Reason: reason, Status: status, Err: err}
	}
	return livekit.CallInfo{ParticipantIdentity: identity}, nil
}

func (o *outbound) Hangup(ctx context.Context, room livekit.RoomInfo) error {
	identity := room.AgentIdentity
	if identity == "" {
		o.mu.Lock()
		identity = o.identity
		o.mu.Unlock()
	}
	if identity == "" {
		return nil
	}
	return o.api.RemoveParticipant(ctx, room.Room, identity)
}

// inbound fires the trigger that makes the agent call tamesu's number; the
// dispatch rule drops the call into the inbound room.
type inbound struct {
	cfg    Config
	api    API
	render func(string) (string, error)
	unlock func()
	once   sync.Once
}

func (i *inbound) release() { i.once.Do(i.unlock) }

func (i *inbound) MaxCallDuration() time.Duration { return i.cfg.MaxCallDuration }

func (i *inbound) Place(ctx context.Context, room livekit.RoomInfo) (livekit.CallInfo, error) {
	tr := i.cfg.Trigger
	switch {
	case tr.HTTP != nil:
		if err := i.httpTrigger(ctx, tr.HTTP); err != nil {
			return livekit.CallInfo{}, &livekit.CallError{Reason: livekit.EndedByError, Err: err}
		}
	case tr.Command != "":
		if err := i.commandTrigger(ctx, tr.Command, room); err != nil {
			return livekit.CallInfo{}, &livekit.CallError{Reason: livekit.EndedByError, Err: err}
		}
	}
	// Any SIP participant that arrives is the caller.
	return livekit.CallInfo{}, nil
}

func (i *inbound) Hangup(ctx context.Context, room livekit.RoomInfo) error {
	if room.AgentIdentity == "" {
		return nil
	}
	return i.api.RemoveParticipant(ctx, room.Room, room.AgentIdentity)
}

// httpTrigger renders and sends the request; a non-2xx answer is an error.
func (i *inbound) httpTrigger(ctx context.Context, t *HTTPTrigger) error {
	u, err := i.render(t.URL)
	if err != nil {
		return fmt.Errorf("trigger url: %w", err)
	}
	method := t.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if t.Body != nil {
		rendered, err := renderValue(t.Body, i.render)
		if err != nil {
			return fmt.Errorf("trigger body: %w", err)
		}
		b, err := json.Marshal(rendered)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range t.Headers {
		rv, err := i.render(v)
		if err != nil {
			return fmt.Errorf("trigger header %s: %w", k, err)
		}
		req.Header.Set(k, rv)
	}
	res, err := i.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("trigger: %s answered %s: %s", u, res.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// commandTrigger runs the shell command with the call details in the
// environment (TAMESU_INBOUND_NUMBER, TAMESU_ROOM).
func (i *inbound) commandTrigger(ctx context.Context, command string, room livekit.RoomInfo) error {
	rendered, err := i.render(command)
	if err != nil {
		return fmt.Errorf("trigger command: %w", err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", rendered)
	cmd.Env = append(os.Environ(), "TAMESU_INBOUND_NUMBER="+i.cfg.InboundNumber, "TAMESU_ROOM="+room.Room)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("trigger command: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// renderValue renders every string inside a JSON-like value.
func renderValue(v any, render func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		return render(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			r, err := renderValue(val, render)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			r, err := renderValue(val, render)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}
