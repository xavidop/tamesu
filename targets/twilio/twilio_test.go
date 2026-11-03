package twilio

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

// fakeAgent is a Media Streams server: it greets after start (when greet is
// set), answers each caller utterance (media then silence) or dtmf with a
// tone whose length encodes the reply index, followed by a mark, and closes
// the stream after closeAfter turns. The fake speech layer decodes the
// length back into the reply text.
type fakeAgent struct {
	mu         sync.Mutex
	replies    []string
	i          int
	greet      bool
	closeAfter int

	starts  []message
	headers []http.Header
	chunks  int
	dtmf    []string
	marks   []string // marks the caller acknowledged
	stopped bool
}

func replyTone(i int) []int16 {
	return audio.Tone(Rate, 440, time.Duration(i+1)*100*time.Millisecond, 0.3).Samples
}

func (f *fakeAgent) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	send := func(m message) {
		b, _ := json.Marshal(m)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	var streamSid string
	turns := 0
	var silence *time.Timer
	answer := func() {
		f.mu.Lock()
		i := f.i
		f.i++
		turns++
		closeNow := f.closeAfter > 0 && turns >= f.closeAfter
		f.mu.Unlock()
		pcm := replyTone(i)
		for off := 0; off < len(pcm); off += 160 {
			end := min(off+160, len(pcm))
			send(message{Event: "media", StreamSid: streamSid, Media: &mediaPayload{Payload: base64.StdEncoding.EncodeToString(g711.EncodeMuLaw(pcm[off:end]))}})
		}
		send(message{Event: "mark", StreamSid: streamSid, Mark: &markPayload{Name: "turn-" + string(rune('a'+i))}})
		if closeNow {
			_ = conn.Close(websocket.StatusNormalClosure, "bye")
		}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Event {
		case "start":
			streamSid = m.StreamSid
			f.mu.Lock()
			f.starts = append(f.starts, m)
			greet := f.greet
			f.mu.Unlock()
			if greet {
				answer()
			}
		case "media":
			f.mu.Lock()
			f.chunks++
			f.mu.Unlock()
			if silence != nil {
				silence.Stop()
			}
			silence = time.AfterFunc(250*time.Millisecond, answer)
		case "dtmf":
			f.mu.Lock()
			f.dtmf = append(f.dtmf, m.DTMF.Digit)
			f.mu.Unlock()
		case "mark":
			f.mu.Lock()
			f.marks = append(f.marks, m.Mark.Name)
			f.mu.Unlock()
		case "stop":
			f.mu.Lock()
			f.stopped = true
			f.mu.Unlock()
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
	i := (len(req.Audio.Samples)+Rate/20)/(Rate/10) - 1
	if i < 0 || i >= len(f.replies) {
		return target.Transcript{Text: "unknown reply"}, nil
	}
	return target.Transcript{Text: f.replies[i]}, nil
}

func newTarget(t *testing.T, f *fakeAgent, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	c := Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/media", Headers: map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}, Parameters: map[string]string{"agent": "support", "caller": "{{secrets.TOKEN}}"}, From: "+15550001", To: "+15550002", Timeout: 5 * time.Second, Idle: 400 * time.Millisecond}
	if cfg != nil {
		cfg(&c)
	}
	return New("phone", c)
}

func runCtx(t *testing.T, replies []string) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Speech: fakeSpeech{replies: replies}, Voice: &target.Voice{Hint: "en-US-female", Language: "en"},
		Render: func(s string) (string, error) { return strings.ReplaceAll(s, "{{secrets.TOKEN}}", "tok-1"), nil }}
}

func TestConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{SkipDone: true, New: func(t *testing.T, replies []string) target.Target {
		f := &fakeAgent{replies: replies}
		return &speechTarget{Target: newTarget(t, f, nil), replies: replies}
	}})
}

// speechTarget gives the harness's bare RunContext the speech layer.
type speechTarget struct {
	target.Target
	replies []string
}

func (s *speechTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Speech = fakeSpeech{replies: s.replies}
	return s.Target.Start(ctx, run)
}

func TestCallWithGreetingMarksAndDTMF(t *testing.T) {
	replies := []string{"Welcome to Mochi Shoes", "Our window is 30 days", "Goodbye"}
	f := &fakeAgent{replies: replies, greet: true, closeAfter: 3}
	ctx := context.Background()
	rc := runCtx(t, replies)
	sess, err := newTarget(t, f, nil).Start(ctx, rc)
	require.NoError(t, err)

	f.mu.Lock()
	require.Len(t, f.starts, 1)
	start := f.starts[0].Start
	f.mu.Unlock()
	assert.Equal(t, "audio/x-mulaw", start.MediaFormat.Encoding)
	assert.Equal(t, 8000, start.MediaFormat.SampleRate)
	assert.Equal(t, map[string]string{"agent": "support", "caller": "tok-1", "From": "+15550001", "To": "+15550002"}, start.CustomParameters, "parameters render and carry From/To")
	assert.True(t, strings.HasPrefix(start.StreamSid, "MZ"))
	assert.Equal(t, "Bearer tok-1", f.headers[0].Get("Authorization"))

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Can I return these shoes?", DTMF: "1"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days", turn.Text, "the greeting was read at start, this is the reply")
	assert.Equal(t, "stt", turn.TranscriptSource)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.NotEmpty(t, turn.Audio)

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Goodbye", turn.Text)
	assert.True(t, turn.Done, "the agent closed the stream")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, WAVFile, arts.Files[WAVFile])
	assert.Equal(t, 2.0, arts.Metrics["turns"])
	assert.Equal(t, 3.0, arts.Metrics["marks"], "greeting and two replies")
	assert.Contains(t, arts.Metrics, "greeting_ms")
	assert.Contains(t, arts.Metrics, "time_to_first_audio_ms")
	assert.Contains(t, arts.Metrics, "call_duration_ms")
	assert.Greater(t, arts.Metrics["agent_speech_ms"], 500.0)
	assert.Greater(t, arts.Metrics["user_speech_ms"], 100.0)
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["start"])
	assert.Equal(t, 1, kinds["greeting"])
	assert.Equal(t, 1, kinds["dtmf"])
	assert.GreaterOrEqual(t, kinds["mark"], 2)

	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []string{"1"}, f.dtmf)
	assert.Greater(t, f.chunks, 20, "20 ms μ-law chunks plus the silence tail")
	assert.GreaterOrEqual(t, len(f.marks), 2, "marks acknowledged once their audio played")
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("no speech", func(t *testing.T) {
		_, err := newTarget(t, &fakeAgent{}, nil).Start(ctx, &target.RunContext{Render: func(s string) (string, error) { return s, nil }})
		assert.ErrorIs(t, err, ErrNoSpeech)
	})
	t.Run("unreachable", func(t *testing.T) {
		_, err := New("phone", Config{URL: "ws://127.0.0.1:1/media", Timeout: time.Second}).Start(ctx, runCtx(t, nil))
		assert.ErrorContains(t, err, "connect")
	})
	t.Run("silent agent times out", func(t *testing.T) {
		f := &fakeAgent{}
		sess, err := newTarget(t, f, func(c *Config) { c.Timeout = 700 * time.Millisecond }).Start(ctx, runCtx(t, nil))
		require.NoError(t, err)
		srvSilent := sess.(*session)
		srvSilent.t.cfg.Idle = 10 * time.Second // never idle out: only the timeout can end it
		f.mu.Lock()
		f.closeAfter = 0
		f.mu.Unlock()
		err = sess.Send(ctx, target.UserTurn{Text: ""})
		assert.ErrorContains(t, err, "did not answer")
	})
}
