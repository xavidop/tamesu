package livekit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/target"
)

// fakeCall is a CallControl whose Place makes a SIP participant ring and
// answer in the fake room, or fails.
type fakeCall struct {
	room     *fakeAudioRoom
	fail     error
	ringFor  time.Duration
	answer   time.Duration
	noAnswer bool
	maxDur   time.Duration

	mu       sync.Mutex
	placed   []RoomInfo
	hungUp   int
	identity string
}

func (c *fakeCall) MaxCallDuration() time.Duration { return c.maxDur }

func (c *fakeCall) Place(_ context.Context, room RoomInfo) (CallInfo, error) {
	c.mu.Lock()
	c.placed = append(c.placed, room)
	c.identity = "sip-agent-" + room.Room
	c.mu.Unlock()
	if c.fail != nil {
		return CallInfo{}, c.fail
	}
	if c.noAnswer {
		return CallInfo{ParticipantIdentity: c.identity}, nil
	}
	go func() {
		r := c.room
		r.mu.Lock()
		r.agentID = c.identity
		r.mu.Unlock()
		// The SIP participant appears while dialing, rings, then answers.
		r.ev.onParticipantJoined(participant{Identity: c.identity, IsSIP: true, Attributes: map[string]string{AttrSIPCallStatus: CallDialing}})
		time.Sleep(20 * time.Millisecond)
		r.ev.onAttributes(c.identity, map[string]string{AttrSIPCallStatus: CallRinging})
		time.Sleep(c.ringFor)
		r.ev.onAttributes(c.identity, map[string]string{AttrSIPCallStatus: CallActive})
		if c.answer > 0 {
			// The IVR greets, then sends a digit.
			time.Sleep(c.answer)
			go r.respond(0)
			time.Sleep(50 * time.Millisecond)
			r.ev.onDTMF(c.identity, "9")
		}
	}()
	return CallInfo{ParticipantIdentity: c.identity}, nil
}

func (c *fakeCall) Hangup(context.Context, RoomInfo) error {
	c.mu.Lock()
	c.hungUp++
	c.mu.Unlock()
	return nil
}

// SendDTMF makes the fake room a dtmfRoom.
func (r *fakeAudioRoom) SendDTMF(_ context.Context, digits string) error {
	r.amu.Lock()
	r.dtmfOut += digits
	r.amu.Unlock()
	return nil
}

func newCallTarget(r *fakeAudioRoom, c *fakeCall) target.Target {
	c.room = r
	r.noText = true
	r.stateless = true
	return New("phone", Config{
		URL: "ws://lk", APIKey: "k", APISecret: "s", Mode: ModeAudio, Room: "tamesu-sip-{{uuid}}", Call: c,
		TurnIdle: 300 * time.Millisecond, JoinTimeout: 500 * time.Millisecond, TurnTimeout: 5 * time.Second,
		connect: r.connect,
	})
}

func kinds(events []target.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func detail(events []target.Event, kind string) string {
	for _, e := range events {
		if e.Kind == kind {
			return e.Detail
		}
	}
	return ""
}

func TestCallHappyPath(t *testing.T) {
	// Three replies: the fake agent leaves after its last one, and this call
	// must still be up when tamesu hangs up.
	r := newAudioRoom("Welcome to Mochi Shoes, press 2 for billing", "Billing, how can I help", "anything else?")
	r.noAgent = true // nobody is in the room until the call connects
	c := &fakeCall{ringFor: 150 * time.Millisecond, answer: 200 * time.Millisecond, maxDur: time.Minute}
	sp := &fakeSpeech{stt: "Welcome to Mochi Shoes, press 2 for billing"}
	sess, err := newCallTarget(r, c).Start(context.Background(), audioRunCtx(t, sp, &target.Voice{Seed: 1}))
	require.NoError(t, err)
	require.Len(t, c.placed, 1)
	assert.Equal(t, "tamesu-sip-0000", c.placed[0].Room)
	assert.Equal(t, "k", c.placed[0].APIKey)

	// The IVR speaks first: an empty turn listens for it.
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Welcome to Mochi Shoes, press 2 for billing", turn.Text)
	assert.Equal(t, "stt", turn.TranscriptSource)

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{DTMF: "2"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.False(t, turn.Done)
	r.amu.Lock()
	assert.Equal(t, "2", r.dtmfOut, "digits also go out as SIP DTMF")
	r.amu.Unlock()

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, c.hungUp, "tamesu hangs up when the session closes")
	ev := kinds(arts.Events)
	t.Logf("events: %v", ev)
	t.Logf("metrics: %v", arts.Metrics)
	assert.Equal(t, []string{"joined", "dialing", "agent_joined"}, ev[:3])
	idx := func(kind string) int {
		for i, k := range ev {
			if k == kind {
				return i
			}
		}
		return -1
	}
	assert.Less(t, idx("ringing"), idx("call_active"), "ringing comes before active")
	assert.Less(t, idx("call_active"), idx("agent_speech_start"), "the agent greets after the call is active")
	assert.Greater(t, idx("audio_published"), 0)
	assert.Contains(t, ev, tassert.EventDTMFReceived)
	assert.Equal(t, "9", detail(arts.Events, tassert.EventDTMFReceived))
	assert.Equal(t, EndedByCaller, detail(arts.Events, tassert.EventCallEnded))
	assert.InDelta(t, 170, arts.Metrics["call_setup_ms"], 60, "dial → active")
	assert.InDelta(t, 150, arts.Metrics["ring_ms"], 60, "ringing → active")
	assert.InDelta(t, 600, arts.Metrics["greeting_ms"], 120, "active → the agent's first speech (200 ms, then the fake's 400 ms response delay)")
	assert.Greater(t, arts.Metrics["call_duration_ms"], 300.0)

	// The assertions read the events.
	rr := &tassert.RunResult{Events: arts.Events}
	assert.True(t, tassert.DTMFReceived("9").Evaluate(context.Background(), rr).Pass)
	assert.True(t, tassert.CallEndedBy([]string{"caller"}).Evaluate(context.Background(), rr).Pass)
}

func TestCallBusyAndNoAnswerAreDeadCalls(t *testing.T) {
	for _, tt := range []struct {
		name   string
		call   *fakeCall
		reason string
	}{
		{"busy", &fakeCall{fail: &CallError{Reason: EndedByBusy, Status: 486, Err: errors.New("486 Busy Here")}}, EndedByBusy},
		{"generic failure", &fakeCall{fail: errors.New("trunk rejected the INVITE")}, EndedByError},
		{"nobody picks up", &fakeCall{noAnswer: true}, EndedByNoAnswer},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newAudioRoom("hi")
			r.noAgent = true
			sess, err := newCallTarget(r, tt.call).Start(context.Background(), audioRunCtx(t, &fakeSpeech{}, &target.Voice{Seed: 1}))
			require.NoError(t, err, "a call that does not connect is a result, not an error")
			require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello?"}))
			turn, err := sess.Receive(context.Background())
			require.NoError(t, err)
			assert.True(t, turn.Done, "the first Receive reports the agent gone")
			assert.Empty(t, turn.Text)
			arts, err := sess.Close(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.reason, detail(arts.Events, tassert.EventCallEnded))
			assert.Equal(t, 0, tt.call.hungUp, "nothing to hang up")
			rr := &tassert.RunResult{Events: arts.Events}
			assert.True(t, tassert.CallEndedBy([]string{tt.reason}).Evaluate(context.Background(), rr).Pass)
		})
	}
}

func TestCallAgentHangsUpAndTransfers(t *testing.T) {
	r := newAudioRoom("one moment, transferring you", "bye")
	r.noAgent = true
	c := &fakeCall{ringFor: 20 * time.Millisecond, answer: 50 * time.Millisecond, maxDur: time.Minute}
	sess, err := newCallTarget(r, c).Start(context.Background(), audioRunCtx(t, &fakeSpeech{stt: "one moment"}, &target.Voice{Seed: 1}))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{}))
	_, err = sess.Receive(context.Background())
	require.NoError(t, err)
	// The PBX reports a REFER, then the far end hangs up.
	r.ev.onData(c.identity, TransferTopic, []byte("sip:billing@pbx.example"))
	r.ev.onAttributes(c.identity, map[string]string{AttrSIPCallStatus: CallHangup})
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.True(t, turn.Done)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, EndedByAgent, detail(arts.Events, tassert.EventCallEnded))
	assert.Equal(t, "sip:billing@pbx.example", detail(arts.Events, tassert.EventTransfer))
	assert.Equal(t, 0, c.hungUp, "the agent hung up first")
	a, err := tassert.TransferredTo("^sip:billing@")
	require.NoError(t, err)
	assert.True(t, a.Evaluate(context.Background(), &tassert.RunResult{Events: arts.Events}).Pass)
}

func TestCallTimeoutReason(t *testing.T) {
	r := newAudioRoom("hi")
	r.noAgent = true
	c := &fakeCall{ringFor: 10 * time.Millisecond, maxDur: 30 * time.Millisecond}
	sess, err := newCallTarget(r, c).Start(context.Background(), audioRunCtx(t, &fakeSpeech{}, &target.Voice{Seed: 1}))
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	r.ev.onAttributes(c.identity, map[string]string{AttrSIPCallStatus: CallHangup})
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, EndedByTimeout, detail(arts.Events, tassert.EventCallEnded), "a hangup past max_call_duration is the cap, not the agent")
	assert.Equal(t, "call busy (SIP 486): 486 Busy Here", (&CallError{Reason: EndedByBusy, Status: 486, Err: errors.New("486 Busy Here")}).Error())
}
