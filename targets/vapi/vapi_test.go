package vapi

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

	"github.com/xavidop/tamesu/audio"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// fakeVapi is the Vapi API and its websocket transport: POST /call answers
// a websocketCallUrl on the same server; the socket greets when greet is
// set, answers each caller utterance with a tone whose length encodes the
// reply index (and, with transcripts, a final transcript message), and ends
// the call with status-update ended after closeAfter turns.
type fakeVapi struct {
	mu          sync.Mutex
	replies     []string
	i           int
	greet       bool
	closeAfter  int
	transcripts bool
	longReply   time.Duration
	stopOnBarge bool
	calls       []map[string]any
	auth        []string
	wsAuth      []string
	frames      int
	hangups     int
	speaking    bool
	base        string
}

func replyTone(i int) []int16 {
	return audio.Tone(DefaultSampleRate, 440, time.Duration(i+1)*100*time.Millisecond, 0.3).Samples
}

func (f *fakeVapi) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.calls = append(f.calls, body)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		n := len(f.calls)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer key-1" {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "call-1", "status": "queued", "transport": map[string]any{"provider": "vapi.websocket", "websocketCallUrl": "ws" + strings.TrimPrefix(f.base, "http") + "/transport/" + string(rune('0'+n))}})
	})
	mux.HandleFunc("/transport/", f.ws)
	return mux
}

func (f *fakeVapi) ws(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.wsAuth = append(f.wsAuth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	sendJSON := func(m map[string]any) {
		b, _ := json.Marshal(m)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	turns := 0
	var silence *time.Timer
	answer := func() {
		f.mu.Lock()
		i := f.i
		f.i++
		turns++
		closeNow := f.closeAfter > 0 && turns >= f.closeAfter
		reply := "no more replies"
		if i < len(f.replies) {
			reply = f.replies[i]
		}
		f.speaking = true
		f.mu.Unlock()
		pcm := replyTone(i)
		if f.longReply > 0 {
			pcm = audio.Tone(DefaultSampleRate, 440, f.longReply, 0.3).Samples
		}
		sendJSON(map[string]any{"type": "speech-update", "status": "started", "role": "assistant"})
		step := DefaultSampleRate / 50
		for off := 0; off < len(pcm); off += step {
			end := min(off+step, len(pcm))
			f.mu.Lock()
			cut := !f.speaking
			f.mu.Unlock()
			if cut {
				break
			}
			_ = conn.Write(ctx, websocket.MessageBinary, pcm16ToBytes(pcm[off:end]))
			if f.longReply > 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		f.mu.Lock()
		f.speaking = false
		f.mu.Unlock()
		if f.transcripts {
			sendJSON(map[string]any{"type": "transcript", "role": "assistant", "transcriptType": "partial", "transcript": "no"})
			sendJSON(map[string]any{"type": "transcript", "role": "assistant", "transcriptType": "final", "transcript": reply})
		}
		sendJSON(map[string]any{"type": "speech-update", "status": "stopped", "role": "assistant"})
		if closeNow {
			sendJSON(map[string]any{"type": "status-update", "status": "ended", "endedReason": "assistant-ended-call"})
		}
	}
	if f.greet {
		go answer()
	}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageBinary {
			f.mu.Lock()
			f.frames++
			barge := f.speaking && f.stopOnBarge
			if barge {
				f.speaking = false
			}
			f.mu.Unlock()
			if barge {
				sendJSON(map[string]any{"type": "user-interrupted"})
			}
			if silence != nil {
				silence.Stop()
			}
			silence = time.AfterFunc(250*time.Millisecond, answer)
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "hangup" {
			f.mu.Lock()
			f.hangups++
			f.mu.Unlock()
			return
		}
	}
}

// fakeSpeech synthesises a tone per character and transcribes a reply tone
// back into its text by length.
type fakeSpeech struct{ replies []string }

func (fakeSpeech) Synthesize(_ context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	tone := audio.Tone(48000, 220, time.Duration(len(req.Text))*8*time.Millisecond, 0.4)
	return target.Audio{Rate: 48000, Samples: tone.Samples}, nil
}

func (f fakeSpeech) Transcribe(_ context.Context, req target.TranscribeRequest) (target.Transcript, error) {
	i := (len(req.Audio.Samples)+DefaultSampleRate/20)/(DefaultSampleRate/10) - 1
	if i < 0 || i >= len(f.replies) {
		return target.Transcript{Text: "unknown reply"}, nil
	}
	return target.Transcript{Text: f.replies[i]}, nil
}

func newTarget(t *testing.T, f *fakeVapi, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	f.base = srv.URL
	c := Config{BaseURL: srv.URL, APIKey: "{{secrets.VAPI}}", AssistantID: "asst-1", Variables: map[string]string{"name": "{{vars.name}}"}, Timeout: 5 * time.Second, Idle: 400 * time.Millisecond, GreetingTimeout: 400 * time.Millisecond}
	if cfg != nil {
		cfg(&c)
	}
	return New("vapi", c)
}

func runCtx(t *testing.T, replies []string) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Speech: fakeSpeech{replies: replies}, Voice: &target.Voice{Hint: "en-US-female", Language: "en"},
		Render: func(s string) (string, error) {
			s = strings.ReplaceAll(s, "{{secrets.VAPI}}", "key-1")
			return strings.ReplaceAll(s, "{{vars.name}}", "Marta"), nil
		}}
}

type speechTarget struct {
	target.Target
	replies []string
}

func (s *speechTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Speech = fakeSpeech{replies: s.replies}
	run.Render = func(str string) (string, error) {
		str = strings.ReplaceAll(str, "{{secrets.VAPI}}", "key-1")
		return strings.ReplaceAll(str, "{{vars.name}}", "Marta"), nil
	}
	return s.Target.Start(ctx, run)
}

func TestConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{SkipDone: true, New: func(t *testing.T, replies []string) target.Target {
		return &speechTarget{Target: newTarget(t, &fakeVapi{replies: replies}, nil), replies: replies}
	}})
}

func TestCallWithGreetingAndTranscripts(t *testing.T) {
	replies := []string{"Welcome to Mochi Shoes", "Our window is 30 days", "Goodbye"}
	f := &fakeVapi{replies: replies, greet: true, closeAfter: 3, transcripts: true}
	ctx := context.Background()
	sess, err := newTarget(t, f, nil).Start(ctx, runCtx(t, replies))
	require.NoError(t, err)

	f.mu.Lock()
	require.Len(t, f.calls, 1)
	call := f.calls[0]
	f.mu.Unlock()
	assert.Equal(t, "asst-1", call["assistantId"])
	assert.Equal(t, map[string]any{"provider": "vapi.websocket", "audioFormat": map[string]any{"format": "pcm_s16le", "container": "raw", "sampleRate": float64(16000)}}, call["transport"])
	assert.Equal(t, map[string]any{"variableValues": map[string]any{"name": "Marta"}}, call["assistantOverrides"], "variables render into variableValues")
	assert.Equal(t, "Bearer key-1", f.auth[0])
	assert.Equal(t, "Bearer key-1", f.wsAuth[0])

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return these shoes?", DTMF: "1"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text)
	assert.Equal(t, "agent", turn.TranscriptSource, "the assistant's final transcript is the text")
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.NotEmpty(t, turn.Audio)
	raws, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.NotEmpty(t, raws, "the control messages of the turn are recorded")

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Goodbye", turn.Text)
	assert.True(t, turn.Done, "status-update ended")
	assert.Error(t, sess.Send(ctx, target.UserTurn{Text: "still there?"}), "the call has ended")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, WAVFile, arts.Files[WAVFile])
	assert.Equal(t, 2.0, arts.Metrics["turns"])
	assert.Contains(t, arts.Metrics, "greeting_ms")
	assert.Contains(t, arts.Metrics, "time_to_first_audio_ms")
	assert.Contains(t, arts.Metrics, "call_duration_ms")
	assert.Greater(t, arts.Metrics["agent_speech_ms"], 500.0)
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["call"])
	assert.Equal(t, 1, kinds["greeting"])
	assert.Equal(t, 1, kinds["dtmf_unsupported"])
	assert.Equal(t, 1, kinds["ended_reason"])
	assert.Equal(t, 0, kinds["hangup"], "no hangup after the assistant ended the call")
}

func TestTranscribesWithoutTranscripts(t *testing.T) {
	replies := []string{"Our window is 30 days"}
	f := &fakeVapi{replies: replies}
	ctx := context.Background()
	sess, err := newTarget(t, f, nil).Start(ctx, runCtx(t, replies))
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "return?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text)
	assert.Equal(t, "stt", turn.TranscriptSource)
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, 1, f.hangups, "tamesu hung up")
	assert.Greater(t, f.frames, 10, "the caller's speech arrived as PCM frames")
	_ = arts
}

func TestBargeIn(t *testing.T) {
	replies := []string{"a long reply", "ok"}
	f := &fakeVapi{replies: replies, longReply: 2 * time.Second, stopOnBarge: true}
	ctx := context.Background()
	rc := runCtx(t, replies)
	rc.Voice.InterruptProbability = 1
	rc.Voice.Seed = 7
	sess, err := newTarget(t, f, nil).Start(ctx, rc)
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.True(t, turn.Interrupted, "the caller decided to talk over the assistant")
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "wait"}))
	_, err = sess.Receive(ctx)
	require.NoError(t, err)
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1.0, arts.Metrics["interruptions"])
	assert.Contains(t, arts.Metrics, "interruption_stop_ms")
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["user_interrupted"])
	assert.Equal(t, 1, kinds["interruption_stopped"])
}

func TestStartErrors(t *testing.T) {
	f := &fakeVapi{}
	_, err := newTarget(t, f, func(c *Config) { c.APIKey = "{{secrets.OTHER}}" }).Start(context.Background(), runCtx(t, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")

	_, err = newTarget(t, f, func(c *Config) { c.AssistantID = "" }).Start(context.Background(), runCtx(t, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one of assistant_id, assistant or squad_id")

	rc := runCtx(t, nil)
	rc.Speech = nil
	_, err = newTarget(t, f, nil).Start(context.Background(), rc)
	require.ErrorIs(t, err, ErrNoSpeech)

	// A transient assistant renders its templates.
	_, err = newTarget(t, f, func(c *Config) {
		c.AssistantID = ""
		c.Assistant = map[string]any{"firstMessage": "Hi {{vars.name}}", "model": map[string]any{"provider": "openai", "model": "gpt-4o"}}
	}).Start(context.Background(), runCtx(t, nil))
	require.NoError(t, err)
	f.mu.Lock()
	last := f.calls[len(f.calls)-1]
	f.mu.Unlock()
	assert.Equal(t, "Hi Marta", last["assistant"].(map[string]any)["firstMessage"])
}

func TestPCMRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	assert.Equal(t, in, bytesToPCM16(pcm16ToBytes(in)))
}
