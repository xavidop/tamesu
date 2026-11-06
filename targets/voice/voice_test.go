package voice

import (
	"context"
	"encoding/base64"
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
	"github.com/xavidop/tamesu/audio/g711"
	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// platform is a hosted voice platform: POST /call creates a call and
// answers its websocket URL; the socket greets when greet is set, answers
// each caller utterance with a tone whose length encodes the reply index
// (binary frames, or base64 inside JSON with jsonFrames), sends final
// transcripts when transcripts is set, and ends the call after closeAfter
// turns with a status message.
type platform struct {
	mu          sync.Mutex
	replies     []string
	i           int
	greet       bool
	closeAfter  int
	transcripts bool
	jsonFrames  bool
	mulaw       bool
	longReply   time.Duration
	stopOnBarge bool
	calls       []map[string]any
	auth        []string
	wsAuth      []string
	opens       []map[string]any
	dtmf        []string
	frames      int
	hangups     int
	speaking    bool
	base        string
}

func (p *platform) rate() int {
	if p.mulaw {
		return 8000
	}
	return 16000
}

func (p *platform) replyTone(i int) []int16 {
	return audio.Tone(p.rate(), 440, time.Duration(i+1)*100*time.Millisecond, 0.3).Samples
}

func (p *platform) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.calls = append(p.calls, body)
		p.auth = append(p.auth, r.Header.Get("Authorization"))
		n := len(p.calls)
		p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer key-1" {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "call-1", "transport": map[string]any{"websocketCallUrl": "ws" + strings.TrimPrefix(p.base, "http") + "/transport/" + string(rune('0'+n))}})
	})
	mux.HandleFunc("/transport/", p.ws)
	return mux
}

func (p *platform) encode(pcm []int16) []byte {
	if p.mulaw {
		return g711.EncodeMuLaw(pcm)
	}
	return pcm16ToBytes(pcm)
}

func (p *platform) ws(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.wsAuth = append(p.wsAuth, r.Header.Get("Authorization"))
	p.mu.Unlock()
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
	sendAudio := func(chunk []int16) {
		if p.jsonFrames {
			sendJSON(map[string]any{"audio_event": map[string]any{"audio_base_64": base64.StdEncoding.EncodeToString(p.encode(chunk))}})
			return
		}
		_ = conn.Write(ctx, websocket.MessageBinary, p.encode(chunk))
	}
	turns := 0
	var silence *time.Timer
	answer := func() {
		p.mu.Lock()
		i := p.i
		p.i++
		turns++
		closeNow := p.closeAfter > 0 && turns >= p.closeAfter
		reply := "no more replies"
		if i < len(p.replies) {
			reply = p.replies[i]
		}
		p.speaking = true
		p.mu.Unlock()
		pcm := p.replyTone(i)
		if p.longReply > 0 {
			pcm = audio.Tone(p.rate(), 440, p.longReply, 0.3).Samples
		}
		step := p.rate() / 50
		for off := 0; off < len(pcm); off += step {
			end := min(off+step, len(pcm))
			p.mu.Lock()
			cut := !p.speaking
			p.mu.Unlock()
			if cut {
				break
			}
			sendAudio(pcm[off:end])
			if p.longReply > 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		p.mu.Lock()
		p.speaking = false
		p.mu.Unlock()
		if p.transcripts {
			sendJSON(map[string]any{"type": "transcript", "role": "assistant", "transcriptType": "partial", "transcript": "no"})
			sendJSON(map[string]any{"type": "transcript", "role": "assistant", "transcriptType": "final", "transcript": reply})
			sendJSON(map[string]any{"type": "tool-calls", "toolCallList": []any{map[string]any{"function": map[string]any{"name": "lookup", "arguments": map[string]any{"q": reply}}}}})
		}
		if closeNow {
			sendJSON(map[string]any{"type": "status-update", "status": "ended", "endedReason": "assistant-ended-call"})
		}
	}
	if p.greet {
		go answer()
	}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		isAudio := kind == websocket.MessageBinary
		var m map[string]any
		if !isAudio && json.Unmarshal(data, &m) == nil {
			if _, ok := m["user_audio_chunk"]; ok {
				isAudio = true
			}
		}
		if isAudio {
			p.mu.Lock()
			p.frames++
			barge := p.speaking && p.stopOnBarge
			if barge {
				p.speaking = false
			}
			p.mu.Unlock()
			if barge {
				sendJSON(map[string]any{"type": "user-interrupted"})
			}
			if silence != nil {
				silence.Stop()
			}
			silence = time.AfterFunc(250*time.Millisecond, answer)
			continue
		}
		switch m["type"] {
		case "hangup":
			p.mu.Lock()
			p.hangups++
			p.mu.Unlock()
			return
		case "open":
			p.mu.Lock()
			p.opens = append(p.opens, m)
			p.mu.Unlock()
		case "dtmf":
			p.mu.Lock()
			p.dtmf = append(p.dtmf, m["digit"].(string))
			p.mu.Unlock()
		}
	}
}

// fakeSpeech synthesises a tone per character and transcribes a reply tone
// back into its text by length.
type fakeSpeech struct {
	replies []string
	rate    int
}

func (fakeSpeech) Synthesize(_ context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	tone := audio.Tone(48000, 220, time.Duration(len(req.Text))*8*time.Millisecond, 0.4)
	return target.Audio{Rate: 48000, Samples: tone.Samples}, nil
}

func (f fakeSpeech) Transcribe(_ context.Context, req target.TranscribeRequest) (target.Transcript, error) {
	i := (len(req.Audio.Samples)+f.rate/20)/(f.rate/10) - 1
	if i < 0 || i >= len(f.replies) {
		return target.Transcript{Text: "unknown reply"}, nil
	}
	return target.Transcript{Text: f.replies[i]}, nil
}

// streamingSpeech is fakeSpeech that also transcribes a stream, counting how
// it was fed.
type streamingSpeech struct {
	fakeSpeech
	mu     sync.Mutex
	opened int
	writes int
	closed int
}

func (s *streamingSpeech) OpenTranscription(_ context.Context, req target.TranscriptionStreamRequest) (target.TranscriptionStream, error) {
	s.mu.Lock()
	s.opened++
	s.mu.Unlock()
	return &fakeStream{s: s, rate: req.Rate}, nil
}

type fakeStream struct {
	s       *streamingSpeech
	rate    int
	samples []int16
}

func (f *fakeStream) Write(_ context.Context, samples []int16) error {
	f.s.mu.Lock()
	f.s.writes++
	f.s.mu.Unlock()
	f.samples = append(f.samples, samples...)
	return nil
}

func (f *fakeStream) Close(ctx context.Context) (target.Transcript, error) {
	f.s.mu.Lock()
	f.s.closed++
	f.s.mu.Unlock()
	return f.s.Transcribe(ctx, target.TranscribeRequest{Audio: target.Audio{Rate: f.rate, Samples: f.samples}})
}

// vapiLike is the configuration of a platform with Vapi's shapes.
func vapiLike(base string) Config {
	return Config{
		URL: "{{session.ws_url}}", Headers: map[string]string{"Authorization": "Bearer {{secrets.KEY}}"},
		SessionOpen: &Request{URL: base + "/call", Headers: map[string]string{"Authorization": "Bearer {{secrets.KEY}}"},
			Body:    map[string]any{"assistantId": "asst-1", "assistantOverrides": map[string]any{"variableValues": map[string]any{"name": "{{vars.name}}"}}},
			Capture: map[string]string{"ws_url": "$.transport.websocketCallUrl", "call_id": "$.id"}},
		Messages: Messages{
			Open:        map[string]any{"type": "open", "call": "{{session.call_id}}"},
			Transcript:  "$[?(@.type=='transcript' && @.role=='assistant' && @.transcriptType=='final')].transcript",
			Done:        "$[?(@.type=='status-update' && @.status=='ended')].status",
			Interrupted: "$[?(@.type=='user-interrupted')].type",
			ToolCalls:   "$[?(@.type=='tool-calls')].toolCallList[*]",
			DTMF:        map[string]any{"type": "dtmf", "digit": "{{digit}}"},
			Close:       map[string]any{"type": "hangup"},
		},
		Timeout: 5 * time.Second, Idle: 400 * time.Millisecond, GreetingTimeout: 400 * time.Millisecond,
	}
}

func newTarget(t *testing.T, p *platform, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	p.base = srv.URL
	c := vapiLike(srv.URL)
	if cfg != nil {
		cfg(&c)
	}
	return New("voice", c)
}

func render(s string) (string, error) {
	s = strings.ReplaceAll(s, "{{secrets.KEY}}", "key-1")
	return strings.ReplaceAll(s, "{{vars.name}}", "Marta"), nil
}

func runCtx(t *testing.T, p *platform) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Speech: fakeSpeech{replies: p.replies, rate: p.rate()}, Voice: &target.Voice{Hint: "en-US-female", Language: "en"}, Render: render}
}

type speechTarget struct {
	target.Target
	p *platform
}

func (s *speechTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Speech = fakeSpeech{replies: s.p.replies, rate: s.p.rate()}
	run.Render = render
	return s.Target.Start(ctx, run)
}

func TestConformance(t *testing.T) {
	for _, jsonFrames := range []bool{false, true} {
		name := "binary"
		if jsonFrames {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{SkipDone: true, New: func(t *testing.T, replies []string) target.Target {
				p := &platform{replies: replies, jsonFrames: jsonFrames, mulaw: jsonFrames}
				tgt := newTarget(t, p, func(c *Config) {
					if jsonFrames {
						c.Audio = Audio{Encoding: EncodingMuLaw, Frames: FramesJSON, Send: map[string]any{"user_audio_chunk": "{{audio}}"}, Receive: "$[*].audio_event.audio_base_64"}
					}
				})
				return &speechTarget{Target: tgt, p: p}
			}})
		})
	}
}

func TestCallWithSessionOpenTranscriptsAndEnd(t *testing.T) {
	replies := []string{"Welcome to Mochi Shoes", "Our window is 30 days", "Goodbye"}
	p := &platform{replies: replies, greet: true, closeAfter: 3, transcripts: true}
	ctx := context.Background()
	sess, err := newTarget(t, p, nil).Start(ctx, runCtx(t, p))
	require.NoError(t, err)
	p.mu.Lock()
	require.Len(t, p.calls, 1)
	call := p.calls[0]
	p.mu.Unlock()
	assert.Equal(t, "asst-1", call["assistantId"])
	assert.Equal(t, map[string]any{"variableValues": map[string]any{"name": "Marta"}}, call["assistantOverrides"], "session.open body renders")
	assert.Equal(t, "Bearer key-1", p.auth[0])
	assert.Equal(t, "Bearer key-1", p.wsAuth[0], "headers render on the websocket")
	time.Sleep(20 * time.Millisecond)
	p.mu.Lock()
	opens := append([]map[string]any(nil), p.opens...)
	p.mu.Unlock()
	require.Len(t, opens, 1)
	assert.Equal(t, "call-1", opens[0]["call"], "messages.open renders the session captures")

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return these shoes?", DTMF: "1"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text)
	assert.Equal(t, "agent", turn.TranscriptSource, "the platform's final transcript is the text")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"q": "Our window is 30 days"}, turn.ToolCalls[0].Args)
	assert.NotEmpty(t, turn.Audio)
	raws, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.NotEmpty(t, raws)

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Goodbye", turn.Text)
	assert.True(t, turn.Done, "messages.done matched")
	assert.Error(t, sess.Send(ctx, target.UserTurn{Text: "still there?"}))

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, WAVFile, arts.Files[WAVFile])
	assert.Equal(t, 2.0, arts.Metrics["turns"])
	assert.Contains(t, arts.Metrics, "greeting_ms")
	assert.Contains(t, arts.Metrics, "time_to_first_audio_ms")
	assert.Greater(t, arts.Metrics["agent_speech_ms"], 500.0)
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["session_open"])
	assert.Equal(t, 1, kinds["connected"])
	assert.Equal(t, 1, kinds["greeting"])
	assert.Equal(t, 1, kinds["dtmf"])
	assert.Equal(t, 1, kinds["ended"])
	assert.Equal(t, 0, kinds["close_message"], "no hangup after the platform ended the call")
	p.mu.Lock()
	assert.Equal(t, []string{"1"}, p.dtmf)
	p.mu.Unlock()
}

func TestJSONFramesMuLawAndSTT(t *testing.T) {
	replies := []string{"Our window is 30 days"}
	p := &platform{replies: replies, jsonFrames: true, mulaw: true}
	cfg := func(c *Config) {
		c.Audio = Audio{Encoding: EncodingMuLaw, Frames: FramesJSON, Send: map[string]any{"user_audio_chunk": "{{audio}}"}, Receive: "$[*].audio_event.audio_base_64"}
	}
	ctx := context.Background()
	sess, err := newTarget(t, p, cfg).Start(ctx, runCtx(t, p))
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "return?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text)
	assert.Equal(t, "stt", turn.TranscriptSource, "no transcript messages: the audio is transcribed")
	_, err = sess.Close(ctx)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Equal(t, 1, p.hangups, "messages.close was sent")
	assert.Greater(t, p.frames, 10, "the caller's speech arrived as JSON audio messages")
}

func TestBargeIn(t *testing.T) {
	replies := []string{"a long reply", "ok"}
	p := &platform{replies: replies, longReply: 2 * time.Second, stopOnBarge: true}
	ctx := context.Background()
	rc := runCtx(t, p)
	rc.Voice.InterruptProbability = 1
	rc.Voice.Seed = 7
	sess, err := newTarget(t, p, nil).Start(ctx, rc)
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.True(t, turn.Interrupted)
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
	assert.Equal(t, 1, kinds["agent_interrupted"])
	assert.Equal(t, 1, kinds["interruption_stopped"])
}

func TestStartErrors(t *testing.T) {
	p := &platform{}
	_, err := newTarget(t, p, func(c *Config) { c.SessionOpen.Headers["Authorization"] = "Bearer other" }).Start(context.Background(), runCtx(t, p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session.open")
	assert.Contains(t, err.Error(), "HTTP 401")

	_, err = newTarget(t, p, func(c *Config) { c.Messages.Done = "$[?(" }).Start(context.Background(), runCtx(t, p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messages.done")

	rc := runCtx(t, p)
	rc.Speech = nil
	_, err = newTarget(t, p, nil).Start(context.Background(), rc)
	require.ErrorIs(t, err, ErrNoSpeech)

	_, err = newTarget(t, p, func(c *Config) { c.SessionOpen = nil; c.URL = "" }).Start(context.Background(), runCtx(t, p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is empty")
}

func TestDefaultsAndHelpers(t *testing.T) {
	assert.Equal(t, 16000, New("a", Config{}).(*Target).cfg.Audio.SampleRate)
	assert.Equal(t, 8000, New("b", Config{Audio: Audio{Encoding: EncodingALaw}}).(*Target).cfg.Audio.SampleRate)
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	assert.Equal(t, in, bytesToPCM16(pcm16ToBytes(in)))
	call, ok := toolCallOf(map[string]any{"name": "lookup", "args": map[string]any{"q": "x"}})
	require.True(t, ok)
	assert.Equal(t, "lookup", call.Name)
	call, ok = toolCallOf(map[string]any{"function": map[string]any{"name": "f", "arguments": `{"a":1}`}})
	require.True(t, ok)
	assert.Equal(t, map[string]any{"a": float64(1)}, call.Args)
	_, ok = toolCallOf(42)
	assert.False(t, ok)
}

func TestStreamingTranscription(t *testing.T) {
	replies := []string{"Our window is 30 days"}
	p := &platform{replies: replies}
	ctx := context.Background()
	sp := &streamingSpeech{fakeSpeech: fakeSpeech{replies: replies, rate: p.rate()}}
	rc := runCtx(t, p)
	rc.Speech = sp
	rc.Voice.TranscriptSource = "stt"
	sess, err := newTarget(t, p, nil).Start(ctx, rc)
	require.NoError(t, err)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "return?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text, "the stream heard the whole reply")
	assert.Equal(t, "stt", turn.TranscriptSource)
	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Contains(t, arts.Metrics, "stt_ms")
	sp.mu.Lock()
	defer sp.mu.Unlock()
	assert.GreaterOrEqual(t, sp.writes, 2, "the agent's audio was fed chunk by chunk as it arrived")
	assert.Equal(t, sp.opened, sp.closed, "every stream opened is closed")
}
