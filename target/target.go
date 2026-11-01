package target

import (
	"context"
	"log/slog"
	"time"
)

// Target is a system under test. Name is the target's name in the suite;
// Start opens one session for one run of one test.
type Target interface {
	Name() string
	Start(ctx context.Context, run *RunContext) (Session, error)
}

// Session is one conversation with a conversational target (rest, livekit,
// sip). Send delivers a user turn; Receive blocks until the agent's turn is
// complete; Close ends the session and returns what was captured.
type Session interface {
	Send(ctx context.Context, turn UserTurn) error
	Receive(ctx context.Context) (AgentTurn, error)
	Close(ctx context.Context) (Artifacts, error)
}

// BrowserSession is one browser session: the browser target exposes a Page
// instead of Send/Receive (its Session methods report that there are no
// turns). See browser.go for Page.
type BrowserSession interface {
	Page() Page
	Close(ctx context.Context) (Artifacts, error)
}

// RunContext is what the runner hands a target for one run.
type RunContext struct {
	// TestName and RunIndex identify the run (RunIndex is 0-based).
	TestName string
	RunIndex int
	// Dir is the run's artifact directory (.tamesu/runs/<ts>/tests/<slug>/run-<n>).
	Dir string
	// Vars are the suite's vars plus --var overrides.
	Vars map[string]string
	// Render expands {{...}} templates with the run's scope (vars, secrets,
	// session values); targets use it for URLs, headers and bodies.
	Render func(s string) (string, error)
	// Logger is a redacting logger scoped to the run.
	Logger *slog.Logger
	// Speech is the speech layer for audio mode (nil in text mode or when
	// no speech models are configured).
	Speech Speech
	// Voice is the caller's voice and behaviors for audio mode (nil otherwise).
	Voice *Voice
}

// UserTurn is what the simulated user sends.
type UserTurn struct {
	Text string
	// Audio is PCM 48 kHz mono for audio mode.
	Audio []byte
	// DTMF digits to send (sip and livekit audio).
	DTMF string
	// At is when the runner handed the turn to the target.
	At time.Time
}

// AgentTurn is what the agent answered, with the transport timestamps the
// metrics are computed from (never from a model).
type AgentTurn struct {
	Text      string
	Audio     []byte
	ToolCalls []ToolCall
	// Done is set when the agent ended the session.
	Done bool
	// Interrupted is set when the caller started its next turn before the
	// agent finished this one (audio mode); Text is what was heard so far.
	Interrupted bool
	// TranscriptSource says where Text came from in audio mode: agent (the
	// agent's own transcription stream) or stt (tamesu transcribed its audio).
	TranscriptSource string
	// Raw is the transport payload (the JSON body, the data message).
	Raw any
	// Status is the HTTP status (rest).
	Status int
	// StartedAt is when the request or utterance started, FirstTokenAt when
	// the first delta or audio frame arrived, EndedAt when the turn completed.
	StartedAt    time.Time
	FirstTokenAt time.Time
	EndedAt      time.Time
}

// ToolCall is a tool the agent called, as the target observed it.
type ToolCall struct {
	Name string
	Args map[string]any
}

// Artifacts is what a session captured.
type Artifacts struct {
	// Files maps an artifact name (transcript.json, audio.wav, trace.har) to
	// its path under the run directory.
	Files map[string]string
	// Metrics are transport-level measurements (metric name to value).
	Metrics map[string]float64
	// Events is the timeline (call setup, DTMF, transfer, hangup, ...).
	Events []Event
}

// Event is one entry of a session timeline.
type Event struct {
	At     time.Time
	Kind   string
	Detail string
}
