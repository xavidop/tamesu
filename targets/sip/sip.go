// Package sip is the phone target: tamesu calls an agent (or is called by
// it) through a LiveKit SIP service and a trunk you bring, and talks to it
// from inside the LiveKit room the call is bridged into, exactly like the
// livekit target in audio mode. This package adds call control (dialing,
// inbound triggers, hangup), the allowed-numbers guard and `tamesu sip
// setup`; the conversation, DTMF, call events and metrics live in
// targets/livekit.
package sip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/livekit"
)

// Directions.
const (
	Outbound = "outbound"
	Inbound  = "inbound"
)

// Defaults.
const (
	DefaultRingTimeout     = 30 * time.Second
	DefaultMaxCallDuration = 5 * time.Minute
	// DefaultInboundRoom is the room the inbound dispatch rule routes calls
	// into (`tamesu sip setup` creates the rule); inbound tests take turns on it.
	DefaultInboundRoom = "tamesu-sip-inbound"
)

// ErrNumberNotAllowed is returned when the number to dial matches none of
// sip.allowed_numbers.
var ErrNumberNotAllowed = errors.New("sip: the number is not in sip.allowed_numbers; tamesu refuses to dial it")

// Config describes a phone target. String fields are templates rendered with
// the run's scope ({{secrets.X}}, {{vars.x}}, {{sip.inbound_number}}).
type Config struct {
	// LiveKitURL is the LiveKit server with the SIP service (sip.livekit_url).
	LiveKitURL string
	APIKey     string
	APISecret  string
	// Direction is outbound (tamesu calls the agent) or inbound (the agent
	// calls tamesu after Trigger).
	Direction string
	// To is the agent's number (outbound), From the caller ID the trunk presents.
	To, From string
	// TrunkID is the LiveKit outbound trunk (sip.outbound_trunk_id).
	TrunkID string
	// RingTimeout bounds the wait for an answer; MaxCallDuration caps the call.
	RingTimeout     time.Duration
	MaxCallDuration time.Duration
	// Trigger makes the agent dial InboundNumber (inbound).
	Trigger *Trigger
	// InboundNumber is the number the trunk routes to tamesu (sip.inbound_number).
	InboundNumber string
	// InboundRoom is the room the dispatch rule routes inbound calls into.
	InboundRoom string
	// AllowedNumbers are the E.164 patterns tamesu may dial.
	AllowedNumbers []string
	// Headers are extra SIP headers on the INVITE (outbound).
	Headers map[string]string

	// API overrides the LiveKit SIP API (tests); nil uses the SDK.
	API API
	// HTTPClient runs inbound HTTP triggers (default 30 s timeout).
	HTTPClient *http.Client
}

// Trigger makes the agent call tamesu: an HTTP request or a shell command.
type Trigger struct {
	HTTP    *HTTPTrigger
	Command string
}

// HTTPTrigger is an HTTP request; its strings are templates.
type HTTPTrigger struct {
	URL     string
	Method  string
	Body    any
	Headers map[string]string
}

// Target is the phone target.
type Target struct {
	name string
	cfg  Config
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.Direction == "" {
		cfg.Direction = Outbound
	}
	if cfg.RingTimeout <= 0 {
		cfg.RingTimeout = DefaultRingTimeout
	}
	if cfg.MaxCallDuration <= 0 {
		cfg.MaxCallDuration = DefaultMaxCallDuration
	}
	if cfg.InboundRoom == "" {
		cfg.InboundRoom = DefaultInboundRoom
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Target{name: name, cfg: cfg}
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// inboundRooms serialises inbound tests on their room: the dispatch rule
// routes every inbound call into the same room.
var inboundRooms sync.Map

// Start renders the config, checks the number against allowed_numbers and
// opens the LiveKit audio session with this target's call control. The call
// itself is placed once tamesu is in the room.
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	cfg := t.cfg
	r := func(s string) (string, error) {
		if run.Render == nil {
			return s, nil
		}
		return run.Render(s)
	}
	var err error
	for _, f := range []struct {
		name string
		dst  *string
	}{{"livekit_url", &cfg.LiveKitURL}, {"to", &cfg.To}, {"from", &cfg.From}, {"trunk_id", &cfg.TrunkID}, {"inbound_number", &cfg.InboundNumber}} {
		if *f.dst, err = r(*f.dst); err != nil {
			return nil, fmt.Errorf("sip %s: %s: %w", t.name, f.name, err)
		}
	}
	if cfg.LiveKitURL == "" {
		return nil, fmt.Errorf("sip %s: sip.livekit_url is required", t.name)
	}
	api := cfg.API
	if api == nil {
		key, err := r(cfg.APIKey)
		if err != nil {
			return nil, fmt.Errorf("sip %s: api_key: %w", t.name, err)
		}
		secret, err := r(cfg.APISecret)
		if err != nil {
			return nil, fmt.Errorf("sip %s: api_secret: %w", t.name, err)
		}
		if key == "" || secret == "" {
			return nil, fmt.Errorf("sip %s: LIVEKIT_API_KEY and LIVEKIT_API_SECRET are required", t.name)
		}
		api = newSDK(cfg.LiveKitURL, key, secret)
	}
	var ctl livekit.CallControl
	room := "tamesu-sip-" + uuid.NewString()
	switch cfg.Direction {
	case Outbound:
		if cfg.To == "" {
			return nil, fmt.Errorf("sip %s: to is required for an outbound call", t.name)
		}
		// A number written in the suite is what the author meant; one that came
		// from a variable or secret must match allowed_numbers.
		if (target.Templated(t.cfg.To) || len(cfg.AllowedNumbers) > 0) && !target.NumberAllowed(cfg.To, cfg.AllowedNumbers) {
			return nil, fmt.Errorf("sip %s: %s: %w", t.name, cfg.To, ErrNumberNotAllowed)
		}
		if cfg.TrunkID == "" {
			return nil, fmt.Errorf("sip %s: trunk_id (or sip.outbound_trunk_id) is required for an outbound call", t.name)
		}
		ctl = &outbound{cfg: cfg, api: api}
	case Inbound:
		if cfg.Trigger == nil || (cfg.Trigger.HTTP == nil && cfg.Trigger.Command == "") {
			return nil, fmt.Errorf("sip %s: an inbound call needs trigger.http or trigger.command", t.name)
		}
		if cfg.InboundNumber == "" {
			return nil, fmt.Errorf("sip %s: sip.inbound_number is required for an inbound call", t.name)
		}
		room = cfg.InboundRoom
		mu, _ := inboundRooms.LoadOrStore(room, &sync.Mutex{})
		lock := mu.(*sync.Mutex)
		lock.Lock()
		ctl = &inbound{cfg: cfg, api: api, render: r, unlock: lock.Unlock}
	default:
		return nil, fmt.Errorf("sip %s: direction must be outbound or inbound, not %q", t.name, cfg.Direction)
	}
	lk := livekit.New(t.name, livekit.Config{
		URL: cfg.LiveKitURL, APIKey: t.cfg.APIKey, APISecret: t.cfg.APISecret,
		Room: room, Mode: livekit.ModeAudio, Call: ctl,
		Identity:    "tamesu-caller-" + uuid.NewString(),
		JoinTimeout: cfg.RingTimeout + 5*time.Second,
	})
	sess, err := lk.Start(ctx, run)
	if err != nil {
		if in, ok := ctl.(*inbound); ok {
			in.release()
		}
		return nil, fmt.Errorf("sip %s: %w", t.name, err)
	}
	if in, ok := ctl.(*inbound); ok {
		return &inboundSession{Session: sess, in: in}, nil
	}
	return sess, nil
}

// inboundSession releases the inbound room when it closes.
type inboundSession struct {
	target.Session
	in *inbound
}

func (s *inboundSession) Close(ctx context.Context) (target.Artifacts, error) {
	defer s.in.release()
	return s.Session.Close(ctx)
}

// callReason maps a SIP API error to busy, no_answer or error.
func callReason(err error) (string, int) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "486"), strings.Contains(msg, "busy"), strings.Contains(msg, "600"):
		return livekit.EndedByBusy, 486
	case strings.Contains(msg, "480"), strings.Contains(msg, "temporarily unavailable"):
		return livekit.EndedByNoAnswer, 480
	case strings.Contains(msg, "408"), strings.Contains(msg, "timeout"), strings.Contains(msg, "no answer"), strings.Contains(msg, "not answered"), strings.Contains(msg, "deadline"):
		return livekit.EndedByNoAnswer, 408
	case strings.Contains(msg, "603"), strings.Contains(msg, "decline"):
		return livekit.EndedByBusy, 603
	case strings.Contains(msg, "404"):
		return livekit.EndedByError, 404
	}
	return livekit.EndedByError, 0
}
