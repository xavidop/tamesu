package pipecat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targets/pipecat/frames"
	"github.com/xavidop/tamesu/targettest"
)

// fakeBot is a Pipecat websocket transport with the protobuf serializer and
// an RTVI observer: send-text gets bot-llm-text deltas and bot-llm-stopped;
// a spoken turn (audio frames then silence) gets bot audio, bot-tts-text and
// bot-stopped-speaking. A message about returns calls lookup_policy, as a
// client-side function when clientTool is set (the bot waits for the result
// and says it back).
type fakeBot struct {
	mu         sync.Mutex
	replies    []string
	i          int
	clientTool bool
	noReady    bool
	noStop     bool // never send the stop message: the turn must end by idle
	closeAfter int  // close the connection after this many turns
	// slow streams every spoken reply as three seconds of audio at real
	// time; yields makes the bot stop and send bot-interrupted when caller
	// audio arrives mid-reply, as a pipeline with an interruption strategy does.
	slow, yields bool
	interrupted  int
	gen, cutGen  int // the current reply and the one the caller cut

	headers []http.Header
	texts   []string // send-text contents
	results []map[string]any
	frames  int // audio frames received
}

func (f *fakeBot) next() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := "no more replies"
	if f.i < len(f.replies) {
		reply = f.replies[f.i]
	}
	f.i++
	return reply, f.i >= len(f.replies)
}

func (f *fakeBot) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	send := func(fr *frames.Frame) {
		b, _ := proto.Marshal(fr)
		_ = conn.Write(ctx, websocket.MessageBinary, b)
	}
	rtvi := func(typ string, data map[string]any) {
		b, _ := json.Marshal(map[string]any{"id": "bot-1", "label": "rtvi-ai", "type": typ, "data": data})
		send(&frames.Frame{Frame: &frames.Frame_Message{Message: &frames.MessageFrame{Data: string(b)}}})
	}
	turns := 0
	speaking := false // the caller's audio is arriving
	answer := func(message string, spoken bool) {
		reply, _ := f.next()
		if spoken {
			rtvi("bot-started-speaking", nil)
		} else {
			rtvi("bot-llm-started", nil)
		}
		if strings.Contains(strings.ToLower(message), "return") {
			if f.clientTool {
				rtvi("llm-function-call", map[string]any{"function_name": "lookup_policy", "tool_call_id": "call_1", "args": map[string]any{"topic": "returns"}})
				// Wait for the result before continuing (handled by the read loop).
				return
			}
			rtvi("llm-function-call-in-progress", map[string]any{"function_name": "lookup_policy", "tool_call_id": "call_1", "arguments": map[string]any{"topic": "returns"}})
		}
		if spoken && f.slow {
			f.mu.Lock()
			f.gen++
			gen := f.gen
			f.mu.Unlock()
			tone := audio.Tone(16000, 440, 20*time.Millisecond, 0.3)
			for i := 0; i < 150; i++ {
				f.mu.Lock()
				superseded, cut := f.gen != gen, f.cutGen == gen
				f.mu.Unlock()
				if superseded {
					return
				}
				if cut {
					rtvi("bot-interrupted", nil)
					rtvi("bot-stopped-speaking", nil)
					return
				}
				send(&frames.Frame{Frame: &frames.Frame_Audio{Audio: &frames.AudioRawFrame{Audio: bytesOf(tone.Samples), SampleRate: 16000, NumChannels: 1}}})
				time.Sleep(20 * time.Millisecond)
			}
			rtvi("bot-tts-text", map[string]any{"text": reply})
		} else {
			for _, w := range strings.SplitAfter(reply, " ") {
				if spoken {
					tone := audio.Tone(16000, 440, 60*time.Millisecond, 0.3)
					send(&frames.Frame{Frame: &frames.Frame_Audio{Audio: &frames.AudioRawFrame{Audio: bytesOf(tone.Samples), SampleRate: 16000, NumChannels: 1}}})
					rtvi("bot-tts-text", map[string]any{"text": strings.TrimSpace(w)})
				} else {
					rtvi("bot-llm-text", map[string]any{"text": w})
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
		if f.noStop {
			return
		}
		if spoken {
			rtvi("bot-stopped-speaking", nil)
		} else {
			rtvi("bot-llm-stopped", nil)
		}
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		var fr frames.Frame
		if proto.Unmarshal(data, &fr) != nil {
			continue
		}
		switch x := fr.Frame.(type) {
		case *frames.Frame_Message:
			var m rtviMessage
			_ = json.Unmarshal([]byte(x.Message.GetData()), &m)
			switch m.Type {
			case "client-ready":
				if !f.noReady {
					rtvi("bot-ready", map[string]any{"version": "1.0.0"})
				}
			case "send-text":
				content, _ := m.Data["content"].(string)
				f.mu.Lock()
				f.texts = append(f.texts, content)
				f.mu.Unlock()
				turns++
				if f.closeAfter > 0 && turns > f.closeAfter {
					return
				}
				answer(content, false)
			case "llm-function-call-result":
				f.mu.Lock()
				f.results = append(f.results, m.Data)
				f.mu.Unlock()
				// The bot continues with the result in hand.
				rtvi("bot-llm-text", map[string]any{"text": "Policy says 30 days."})
				rtvi("bot-llm-stopped", nil)
			}
		case *frames.Frame_Audio:
			f.mu.Lock()
			f.frames++
			f.mu.Unlock()
			silent := true
			for _, b := range x.Audio.GetAudio() {
				if b != 0 {
					silent = false
					break
				}
			}
			if !silent {
				speaking = true
				f.mu.Lock()
				if f.yields && f.gen > 0 && f.cutGen != f.gen {
					f.cutGen = f.gen
					f.interrupted++
				}
				f.mu.Unlock()
			} else if speaking {
				speaking = false
				rtvi("user-stopped-speaking", nil)
				if f.slow {
					// Stream in the background so the caller's barge-in is heard.
					go answer("spoken turn", true)
				} else {
					answer("spoken turn", true)
				}
			}
		}
	}
}

type fakeSpeech struct{}

func (fakeSpeech) Synthesize(_ context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	tone := audio.Tone(48000, 220, time.Duration(len(req.Text))*10*time.Millisecond, 0.4)
	return target.Audio{Rate: 48000, Samples: tone.Samples}, nil
}

func (fakeSpeech) Transcribe(context.Context, target.TranscribeRequest) (target.Transcript, error) {
	return target.Transcript{Text: "transcribed by stt"}, nil
}

func newTarget(t *testing.T, f *fakeBot, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	c := Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", Headers: map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}, Timeout: 5 * time.Second, Idle: 400 * time.Millisecond}
	if cfg != nil {
		cfg(&c)
	}
	return New("bot", c)
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.TOKEN}}", "tok-1"), nil
	}}
}

func TestConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{SkipDone: true, New: func(t *testing.T, replies []string) target.Target {
		return newTarget(t, &fakeBot{replies: replies}, nil)
	}})
}

func TestTextTurn(t *testing.T) {
	f := &fakeBot{replies: []string{"Our window is 30 days.", "Bye!"}}
	ctx := context.Background()
	sess, err := newTarget(t, f, nil).Start(ctx, runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return shoes?"}))
	agent, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", agent.Text)
	require.Len(t, agent.ToolCalls, 1, "a server-side function call is recorded")
	assert.Equal(t, target.ToolCall{Name: "lookup_policy", Args: map[string]any{"topic": "returns"}}, agent.ToolCalls[0])
	assert.False(t, agent.FirstTokenAt.IsZero())
	assert.True(t, agent.EndedAt.Sub(agent.StartedAt) < 300*time.Millisecond, "the turn ended at bot-llm-stopped, not by idle")
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1.0, arts.Metrics["turns"])
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"Can I return shoes?"}, f.texts)
	assert.Equal(t, "Bearer tok-1", f.headers[0].Get("Authorization"))
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "bot_ready")
	assert.Contains(t, kinds, "tool_call")
}

func TestClientToolAnswered(t *testing.T) {
	f := &fakeBot{replies: []string{"Checking."}, clientTool: true}
	ctx := context.Background()
	sess, err := newTarget(t, f, func(c *Config) {
		c.ToolResults = map[string]any{"lookup_policy": map[string]any{"topic": "{{args.topic}}", "window_days": 30}}
	}).Start(ctx, runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "about returns"}))
	agent, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Policy says 30 days.", agent.Text)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.results, 1)
	assert.Equal(t, "lookup_policy", f.results[0]["function_name"])
	assert.Equal(t, "call_1", f.results[0]["tool_call_id"])
	assert.Equal(t, map[string]any{"topic": "returns", "window_days": float64(30)}, f.results[0]["result"])
}

func TestIdleEndsTurn(t *testing.T) {
	f := &fakeBot{replies: []string{"slow bot"}, noStop: true, noReady: true}
	ctx := context.Background()
	sess, err := newTarget(t, f, func(c *Config) { c.Idle = 300 * time.Millisecond }).Start(ctx, runCtx(t))
	require.NoError(t, err)
	started := time.Now()
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "hi"}))
	agent, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "slow bot", agent.Text)
	assert.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond, "ended by idle")
}

func TestAudioTurn(t *testing.T) {
	f := &fakeBot{replies: []string{"Our window is 30 days.", "Bye"}}
	ctx := context.Background()
	rc := runCtx(t)
	rc.Speech = fakeSpeech{}
	rc.Voice = &target.Voice{Hint: "en-US-female", Language: "en"}
	sess, err := newTarget(t, f, func(c *Config) { c.Mode = ModeAudio }).Start(ctx, rc)
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return these?"}))
	agent, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", agent.Text, "bot-tts-text joined")
	assert.Equal(t, "agent", agent.TranscriptSource)
	assert.False(t, agent.FirstTokenAt.IsZero())
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, WAVFile, arts.Files[WAVFile])
	assert.Greater(t, arts.Metrics["agent_speech_ms"], 100.0)
	assert.Greater(t, arts.Metrics["user_speech_ms"], 100.0)
	assert.Contains(t, arts.Metrics, "time_to_first_audio_ms")
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Greater(t, f.frames, 10, "20 ms frames, then the silence tail")
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("no speech in audio mode", func(t *testing.T) {
		_, err := newTarget(t, &fakeBot{}, func(c *Config) { c.Mode = ModeAudio }).Start(ctx, runCtx(t))
		assert.ErrorIs(t, err, ErrNoSpeech)
	})
	t.Run("nothing listening", func(t *testing.T) {
		_, err := New("bot", Config{URL: "ws://127.0.0.1:1/ws", Timeout: time.Second}).Start(ctx, runCtx(t))
		assert.Error(t, err)
	})
	t.Run("closed mid-session", func(t *testing.T) {
		f := &fakeBot{replies: []string{"one", "two"}, closeAfter: 1}
		sess, err := newTarget(t, f, nil).Start(ctx, runCtx(t))
		require.NoError(t, err)
		require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "a"}))
		_, err = sess.Receive(ctx)
		require.NoError(t, err)
		err = sess.Send(ctx, target.UserTurn{Text: "b"})
		assert.ErrorContains(t, err, "closed")
	})
	t.Run("timeout without an answer", func(t *testing.T) {
		f := &fakeBot{replies: nil, noStop: true, noReady: true}
		f.replies = []string{""}
		sess, err := newTarget(t, f, func(c *Config) { c.Timeout = 300 * time.Millisecond }).Start(ctx, runCtx(t))
		require.NoError(t, err)
		err = sess.Send(ctx, target.UserTurn{Text: "hello"})
		if err != nil {
			assert.ErrorContains(t, err, "did not answer")
		}
	})
}

func TestDecodeAndRender(t *testing.T) {
	ev := decode(websocket.MessageText, []byte(`{"label":"rtvi-ai","type":"bot-ready"}`))
	assert.Equal(t, "message", ev.kind)
	assert.Equal(t, "bot-ready", ev.msg.Type)
	assert.Equal(t, "unparsable", decode(websocket.MessageBinary, []byte{0xff, 0xfe}).kind)
	stereo := bytesOf([]int16{100, 300, -100, -300})
	assert.Equal(t, []int16{200, -200}, pcmOf(stereo, 2), "channels are mixed down")
	assert.Equal(t, map[string]any{"a": "x 3"}, renderArgs(map[string]any{"a": "x {{args.n}}"}, map[string]any{"n": 3.0}))
}

func TestBargeIn(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		yields bool
	}{{"the bot yields", true}, {"the bot keeps talking", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBot{replies: []string{"a", "b", "c"}, slow: true, yields: tc.yields}
			rc := runCtx(t)
			rc.Speech = fakeSpeech{}
			rc.Voice = &target.Voice{Hint: "en-US-female", Language: "en", InterruptProbability: 1, Seed: 7}
			sess, err := newTarget(t, f, func(c *Config) { c.Mode = ModeAudio; c.Timeout = 8 * time.Second }).Start(ctx, rc)
			require.NoError(t, err)

			require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "hello"}))
			turn, err := sess.Receive(ctx)
			require.NoError(t, err)
			assert.True(t, turn.Interrupted, "the caller stopped listening partway through the long reply")

			require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "wait"}))
			_, err = sess.Receive(ctx)
			require.NoError(t, err)
			arts, err := sess.Close(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1.0, arts.Metrics["interruptions"], "%v", arts.Metrics)
			stop, ok := arts.Metrics["interruption_stop_ms_p95"]
			require.True(t, ok, "the stop time is measured: %v", arts.Metrics)
			kinds := map[string]string{}
			for _, e := range arts.Events {
				kinds[e.Kind] = e.Detail
			}
			assert.Contains(t, kinds, "interrupting")
			assert.Contains(t, kinds, "interruption")
			assert.Contains(t, kinds, "interruption_stopped")
			if tc.yields {
				assert.Less(t, stop, 1500.0, "the bot yielded within a second of the caller speaking")
				assert.Contains(t, kinds, "bot_interrupted")
				assert.Contains(t, kinds["interruption_stopped"], "the bot yielded")
			} else {
				assert.Greater(t, stop, 500.0, "the audio already sent played out after the caller spoke")
				assert.Contains(t, kinds["interruption_stopped"], "never yielded")
			}
		})
	}
}
