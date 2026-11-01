package livekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/metrics"
)

// The call hooks: a phone call (the sip target) is a LiveKit room with a SIP
// participant in it, so the sip target reuses this session and only adds
// call control. Call status comes from the SIP participant's attributes.

// RoomInfo is the room a call is placed into.
type RoomInfo struct {
	URL, APIKey, APISecret string
	Room                   string
	// Identity is the simulated caller's identity in the room.
	Identity string
}

// CallInfo is what placing a call returned.
type CallInfo struct {
	// ParticipantIdentity is the SIP participant to treat as the agent; empty
	// means any participant of kind sip (or the identity prefix).
	ParticipantIdentity string
}

// CallControl places and ends the call of a session. The sip target
// implements it with the LiveKit SIP API.
type CallControl interface {
	// Place dials out (or triggers the agent to dial in) once tamesu is in
	// the room. It returns when the call is set up as far as the API knows;
	// the session then waits for the SIP participant.
	Place(ctx context.Context, room RoomInfo) (CallInfo, error)
	// Hangup ends the call when the session closes.
	Hangup(ctx context.Context, room RoomInfo) error
	// MaxCallDuration is the cap the call was placed with (0: none); a
	// hangup after it is reported as call_ended=timeout.
	MaxCallDuration() time.Duration
}

// Call end reasons (assert.CallEndedBy).
const (
	EndedByAgent    = "agent"
	EndedByCaller   = "caller"
	EndedByTimeout  = "timeout"
	EndedByError    = "error"
	EndedByNoAnswer = "no_answer"
	EndedByBusy     = "busy"
)

// CallError is a call that did not connect. Reason is busy, no_answer or
// error; Status is the SIP status code when known (486, 480, 408, ...).
type CallError struct {
	Reason string
	Status int
	Err    error
}

func (e *CallError) Error() string {
	var b strings.Builder
	b.WriteString("call " + e.Reason)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (SIP %d)", e.Status)
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *CallError) Unwrap() error { return e.Err }

// SIP participant attributes (github.com/livekit/protocol/livekit/attrs.go)
// and the sip.callStatus values.
const (
	AttrSIPCallStatus = "sip.callStatus"
	AttrSIPCallID     = "sip.callID"
	CallDialing       = "dialing"
	CallRinging       = "ringing"
	CallActive        = "active"
	CallHangup        = "hangup"
	// TransferTopic is a data-packet topic a PBX-side helper (or an agent)
	// can use to report a SIP REFER target.
	TransferTopic = "tamesu.transfer"
)

// callState is the call half of a session.
type callState struct {
	ctl      CallControl
	info     RoomInfo
	placedAt time.Time
	ringAt   time.Time
	activeAt time.Time
	endedAt  time.Time
	endedBy  string
	greeted  bool
	status   string
	identity string // the SIP participant the call returned, if any
}

// placeCall runs CallControl.Place and records the outcome.
func (s *session) placeCall(ctx context.Context) error {
	c := s.call
	s.mu.Lock()
	c.placedAt = time.Now()
	s.eventLocked("dialing", c.info.Room)
	s.mu.Unlock()
	info, err := c.ctl.Place(ctx, c.info)
	if err != nil {
		var ce *CallError
		reason := EndedByError
		if errors.As(err, &ce) && ce.Reason != "" {
			reason = ce.Reason
		}
		s.endCall(reason, err.Error())
		return err
	}
	s.mu.Lock()
	c.identity = info.ParticipantIdentity
	s.mu.Unlock()
	return nil
}

// endCall records who ended the call, once.
func (s *session) endCall(by, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endCallLocked(by, detail)
}

func (s *session) endCallLocked(by, detail string) {
	c := s.call
	if c == nil || c.endedBy != "" {
		return
	}
	c.endedBy = by
	c.endedAt = time.Now()
	if !c.activeAt.IsZero() {
		s.metrics[metrics.CallDurationMs] = ms(c.endedAt.Sub(c.activeAt))
	}
	s.eventLocked(assert.EventCallEnded, by)
	if detail != "" {
		s.eventLocked("call_end_detail", detail)
	}
	s.leftOnce.Do(func() { close(s.agentLeft) })
	s.signal()
}

// callStatus handles a sip.callStatus change of the agent participant.
func (s *session) callStatusLocked(status string) {
	c := s.call
	if c == nil || status == c.status {
		return
	}
	c.status = status
	now := time.Now()
	switch status {
	case CallDialing:
		s.eventLocked("dialing", "")
	case CallRinging:
		c.ringAt = now
		s.eventLocked("ringing", "")
	case CallActive:
		if c.activeAt.IsZero() {
			c.activeAt = now
			s.eventLocked("call_active", "")
			if !c.placedAt.IsZero() {
				s.metrics[metrics.CallSetupMs] = ms(now.Sub(c.placedAt))
			}
			if !c.ringAt.IsZero() {
				s.metrics[metrics.RingMs] = ms(now.Sub(c.ringAt))
			}
		}
	case CallHangup:
		by := EndedByAgent
		if d := c.ctl.MaxCallDuration(); d > 0 && !c.activeAt.IsZero() && now.Sub(c.activeAt) >= d {
			by = EndedByTimeout
		}
		s.endCallLocked(by, "")
	}
}

// greetingLocked records the agent's first speech after the call went active.
func (s *session) greetingLocked(now time.Time) {
	c := s.call
	if c == nil || c.greeted || c.activeAt.IsZero() {
		return
	}
	c.greeted = true
	s.metrics[metrics.GreetingMs] = ms(now.Sub(c.activeAt))
}

// isCallParticipant reports whether p is the far end of the call.
func (s *session) isCallParticipant(p participant) bool {
	if s.call == nil {
		return false
	}
	if s.call.identity != "" {
		return p.Identity == s.call.identity
	}
	return p.IsSIP
}

// hangup ends the call from our side when the session closes.
func (s *session) hangup(ctx context.Context) {
	c := s.call
	s.mu.Lock()
	ended := c.endedBy != ""
	s.mu.Unlock()
	if ended {
		return
	}
	if err := c.ctl.Hangup(ctx, c.info); err != nil {
		s.event("hangup_error", err.Error())
	}
	s.endCall(EndedByCaller, "")
}

// deadCall is the session of a call that never connected: the first Receive
// reports the agent gone, so the run ends with the call_ended event on record.
func (s *session) deadCall() {
	s.mu.Lock()
	s.dead = true
	s.mu.Unlock()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// transferLocked records a SIP REFER target.
func (s *session) transferLocked(to string) {
	to = strings.TrimSpace(to)
	if to == "" {
		return
	}
	s.eventLocked(assert.EventTransfer, to)
}

// dtmfReceivedLocked records digits the agent sent.
func (s *session) dtmfReceivedLocked(identity, digit string) {
	if identity != s.agentID || digit == "" {
		return
	}
	s.eventLocked(assert.EventDTMFReceived, digit)
	s.signal()
}
