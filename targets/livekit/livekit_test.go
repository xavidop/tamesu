package livekit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// fakeRoom is a room with a scripted agent in it. The agent answers every
// text on the data topic with the next reply, going through the LiveKit
// Agents state attributes, unless stateless is set (then only text arrives
// and the idle timeout must end the turn).
type fakeRoom struct {
	mu        sync.Mutex
	ev        events
	cfg       connectConfig
	replies   []string
	i         int
	stateless bool
	chunked   bool
	agentID   string
	joined    bool
	sent      []string
	closed    bool
	noAgent   bool
	delay     time.Duration
}

func (r *fakeRoom) connect(_ context.Context, cfg connectConfig, ev events) (room, error) {
	r.cfg = cfg
	r.ev = ev
	r.agentID = "agent-" + cfg.Room
	if !r.noAgent {
		// The agent is already in the room (dispatched), listening.
		r.joined = true
	}
	return r, nil
}

func (r *fakeRoom) Participants() []participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.joined {
		return nil
	}
	return []participant{{Identity: r.agentID, IsAgent: true, Attributes: map[string]string{AttrAgentState: StateListening}}}
}

func (r *fakeRoom) Disconnect() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *fakeRoom) SendText(_ context.Context, topic, text string) error {
	r.mu.Lock()
	r.sent = append(r.sent, topic+": "+text)
	reply := "no more replies"
	if r.i < len(r.replies) {
		reply = r.replies[r.i]
	}
	r.i++
	last := r.i >= len(r.replies)
	r.mu.Unlock()
	go func() {
		time.Sleep(r.delay)
		if !r.stateless {
			r.ev.onAttributes(r.agentID, map[string]string{AttrAgentState: StateThinking})
			time.Sleep(5 * time.Millisecond)
			r.ev.onAttributes(r.agentID, map[string]string{AttrAgentState: StateSpeaking})
		}
		if r.chunked {
			words := strings.Fields(reply)
			for i, w := range words {
				attrs := map[string]string{"lk.segment_id": "seg-" + w}
				if i == len(words)-1 && !r.stateless {
					attrs[AttrTranscriptionFinal] = "true"
				}
				r.ev.onText(r.agentID, TranscriptionTopic, w, attrs)
				time.Sleep(2 * time.Millisecond)
			}
		} else {
			attrs := map[string]string{}
			if !r.stateless {
				attrs[AttrTranscriptionFinal] = "true"
			}
			r.ev.onText(r.agentID, TranscriptionTopic, reply, attrs)
		}
		if last {
			// The agent hangs up right after its last words, before it
			// ever goes back to listening.
			r.ev.onParticipantLeft(r.agentID)
			return
		}
		if !r.stateless {
			r.ev.onAttributes(r.agentID, map[string]string{AttrAgentState: StateListening})
		}
	}()
	return nil
}

func newTarget(t *testing.T, r *fakeRoom) target.Target {
	t.Helper()
	return New("voice", Config{
		URL: "ws://lk", APIKey: "{{secrets.LIVEKIT_API_KEY}}", APISecret: "{{secrets.LIVEKIT_API_SECRET}}",
		Room: "support-{{uuid}}", AgentName: "support-agent", TurnIdle: 60 * time.Millisecond, JoinTimeout: 300 * time.Millisecond, TurnTimeout: 2 * time.Second,
		connect: r.connect,
	})
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		s = strings.ReplaceAll(s, "{{secrets.LIVEKIT_API_KEY}}", "devkey")
		s = strings.ReplaceAll(s, "{{secrets.LIVEKIT_API_SECRET}}", "secret")
		s = strings.ReplaceAll(s, "{{uuid}}", "0000")
		return s, nil
	}}
}

func TestConformance(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		name := "with-agent-state"
		if stateless {
			name = "idle-timeout"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				return newTarget(t, &fakeRoom{replies: replies, stateless: stateless, chunked: true})
			}})
		})
	}
}

func TestTextTurn(t *testing.T) {
	r := &fakeRoom{replies: []string{"Our window is 30 days.", "Bye now"}, chunked: true}
	sess, err := newTarget(t, r).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	assert.Equal(t, "ws://lk", r.cfg.URL)
	assert.Equal(t, "devkey", r.cfg.APIKey)
	assert.Equal(t, "support-0000", r.cfg.Room)
	assert.Equal(t, "support-agent", r.cfg.AgentName)
	assert.True(t, strings.HasPrefix(r.cfg.Identity, "tamesu-sim-"))

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return shoes?", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text, "chunks are joined with spaces, one per segment id")
	assert.False(t, turn.Done)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.True(t, !turn.EndedAt.Before(turn.StartedAt))
	assert.Equal(t, []string{"lk.chat: Can I return shoes?"}, r.sent)

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "ok bye", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bye now", turn.Text)
	assert.True(t, turn.Done, "the agent left after its last reply")

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.True(t, r.closed)
	assert.Contains(t, arts.Metrics, "agent_join_ms")
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []string{"joined", "agent_joined", "agent_left"}, kinds)
}

func TestGreetingBeforeFirstTurn(t *testing.T) {
	r := &fakeRoom{replies: []string{"Sure, 30 days."}}
	tgt := newTarget(t, r)
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	// The agent greets before the user says anything.
	r.ev.onText(r.agentID, TranscriptionTopic, "Hi, how can I help?", map[string]string{AttrTranscriptionFinal: "true"})
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "returns?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Hi, how can I help? Sure, 30 days.", turn.Text)
}

func TestToolCallsOverData(t *testing.T) {
	r := &fakeRoom{replies: []string{"done"}}
	sess, err := newTarget(t, r).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "book it"}))
	r.ev.onData(r.agentID, ToolCallsTopic, []byte(`{"name":"reschedule_booking","args":{"day":"saturday"}}`))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "reschedule_booking", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"day": "saturday"}, turn.ToolCalls[0].Args)
}

func TestNoAgentJoins(t *testing.T) {
	r := &fakeRoom{noAgent: true}
	_, err := newTarget(t, r).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent joined room support-0000 within 300ms")
	assert.True(t, r.closed, "the room is left when no agent shows up")
}

func TestAgentJoinsLater(t *testing.T) {
	r := &fakeRoom{noAgent: true, replies: []string{"hi"}}
	tgt := newTarget(t, r)
	done := make(chan error, 1)
	go func() {
		_, err := tgt.Start(context.Background(), runCtx(t))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	r.joined = true
	r.mu.Unlock()
	r.ev.onParticipantJoined(participant{Identity: r.agentID, IsAgent: true})
	require.NoError(t, <-done)
}

func TestIdentityPrefix(t *testing.T) {
	r := &fakeRoom{replies: []string{"hi"}}
	tgt := New("voice", Config{URL: "ws://lk", APIKey: "k", APISecret: "s", AgentIdentityPrefix: "other-", JoinTimeout: 100 * time.Millisecond, connect: r.connect})
	_, err := tgt.Start(context.Background(), runCtx(t))
	require.Error(t, err, "the agent's identity does not carry the prefix")
	assert.Contains(t, err.Error(), `agent_identity_prefix="other-"`)
}

func TestConnectErrorsAndAudioMode(t *testing.T) {
	failing := func(context.Context, connectConfig, events) (room, error) {
		return nil, errors.New("dial tcp: connection refused")
	}
	_, err := New("voice", Config{URL: "ws://lk", APIKey: "k", APISecret: "s", connect: failing}).Start(context.Background(), runCtx(t))
	assert.ErrorContains(t, err, "join room")
	_, err = New("voice", Config{URL: "ws://lk", APIKey: "", APISecret: "s", connect: failing}).Start(context.Background(), runCtx(t))
	assert.ErrorContains(t, err, "api_key and api_secret are required")
	_, err = New("voice", Config{URL: "ws://lk", APIKey: "k", APISecret: "s", Mode: ModeAudio, connect: failing}).Start(context.Background(), runCtx(t))
	assert.ErrorContains(t, err, "audio mode lands")
}

func TestParseToolCalls(t *testing.T) {
	assert.Len(t, parseToolCalls([]byte(`[{"name":"a"},{"name":"b","args":{"x":1}}]`)), 2)
	assert.Nil(t, parseToolCalls([]byte(`not json`)))
	assert.Nil(t, parseToolCalls([]byte(`{"args":{}}`)))
}
