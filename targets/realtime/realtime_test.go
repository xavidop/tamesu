package realtime

import (
	"context"
	"encoding/json"
	"fmt"
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

// fakeServer speaks the Realtime protocol: session.created on connect,
// session.updated for session.update, and for each response.create the next
// reply streamed as deltas (text, or audio plus transcript in audio mode)
// closed by response.done. A message about returns calls lookup_policy; the
// last reply calls end_session. beta switches the event names.
type fakeServer struct {
	mu       sync.Mutex
	replies  []string
	i        int
	beta     bool
	audio    bool
	failWith string // "error" sends an error event, "failed" a failed response
	noUpdate bool   // never answer session.update

	sessions []map[string]any
	items    []string
	headers  []http.Header
	paths    []string
	appended int
	commits  int
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.paths = append(f.paths, r.URL.String())
	f.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	send := func(v map[string]any) {
		b, _ := json.Marshal(v)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	send(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_1"}})
	lastMessage := ""
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		switch m["type"] {
		case "session.update":
			f.mu.Lock()
			f.sessions = append(f.sessions, m["session"].(map[string]any))
			f.mu.Unlock()
			if !f.noUpdate {
				send(map[string]any{"type": "session.updated", "session": m["session"]})
			}
		case "conversation.item.create":
			item := m["item"].(map[string]any)
			content := item["content"].([]any)[0].(map[string]any)
			lastMessage, _ = content["text"].(string)
			f.mu.Lock()
			f.items = append(f.items, lastMessage)
			f.mu.Unlock()
		case "input_audio_buffer.append":
			f.mu.Lock()
			f.appended++
			f.mu.Unlock()
		case "input_audio_buffer.commit":
			f.mu.Lock()
			f.commits++
			f.mu.Unlock()
			lastMessage = "spoken turn"
		case "response.create":
			f.respond(send, lastMessage)
		}
	}
}

func (f *fakeServer) respond(send func(map[string]any), message string) {
	f.mu.Lock()
	reply := "no more replies"
	if f.i < len(f.replies) {
		reply = f.replies[f.i]
	}
	f.i++
	last := f.i >= len(f.replies)
	f.mu.Unlock()
	id := fmt.Sprintf("resp_%d", f.i)
	send(map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
	switch f.failWith {
	case "error":
		send(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "bad", "message": "the fake refused"}})
		return
	case "failed":
		send(map[string]any{"type": "response.done", "response": map[string]any{"id": id, "status": "failed", "status_details": map[string]any{"error": map[string]any{"message": "model overloaded"}}}})
		return
	}
	textDelta, audioDelta, transcriptDelta := "response.output_text.delta", "response.output_audio.delta", "response.output_audio_transcript.delta"
	if f.beta {
		textDelta, audioDelta, transcriptDelta = "response.text.delta", "response.audio.delta", "response.audio_transcript.delta"
	}
	words := strings.SplitAfter(reply, " ")
	for _, w := range words {
		if f.audio {
			send(map[string]any{"type": transcriptDelta, "response_id": id, "delta": w})
			tone := audio.Tone(AudioRate, 440, 60*time.Millisecond, 0.3)
			send(map[string]any{"type": audioDelta, "response_id": id, "delta": encodePCM(tone.Samples)})
		} else {
			send(map[string]any{"type": textDelta, "response_id": id, "delta": w})
		}
		time.Sleep(2 * time.Millisecond)
	}
	if strings.Contains(strings.ToLower(message), "return") {
		send(map[string]any{"type": "response.function_call_arguments.done", "response_id": id, "call_id": "call_1", "name": "lookup_policy", "arguments": `{"topic":"returns"}`})
		// The same call again as the item, which must not be recorded twice.
		send(map[string]any{"type": "response.output_item.done", "response_id": id, "item": map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup_policy", "arguments": `{"topic":"returns"}`}})
	}
	if last {
		send(map[string]any{"type": "response.output_item.done", "response_id": id, "item": map[string]any{"type": "function_call", "call_id": "call_end", "name": "end_session", "arguments": "{}"}})
	}
	send(map[string]any{"type": "response.done", "response": map[string]any{"id": id, "status": "completed", "usage": map[string]any{"input_tokens": 12, "output_tokens": 7}}})
}

func newTarget(t *testing.T, f *fakeServer, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	c := Config{
		URL:     "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/realtime",
		Model:   "gpt-realtime",
		Headers: map[string]string{"Authorization": "Bearer {{secrets.OPENAI_API_KEY}}"},
		Timeout: 5 * time.Second,
	}
	if f.beta {
		c.Protocol = ProtocolBeta
	}
	if f.audio {
		c.Mode = ModeAudio
	}
	if cfg != nil {
		cfg(&c)
	}
	return New("rt", c)
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.OPENAI_API_KEY}}", "sk-test"), nil
	}}
}

func TestConformance(t *testing.T) {
	for _, beta := range []bool{false, true} {
		name := "ga"
		if beta {
			name = "beta"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				return newTarget(t, &fakeServer{replies: replies, beta: beta}, nil)
			}})
		})
	}
}

func TestTextTurn(t *testing.T) {
	f := &fakeServer{replies: []string{"Our window is 30 days.", "Bye now."}}
	tgt := newTarget(t, f, func(c *Config) {
		c.SessionUpdate = map[string]any{"instructions": "Be brief", "audio": map[string]any{"output": map[string]any{"voice": "marin"}}}
	})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return shoes?", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text)
	assert.False(t, turn.Done)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.True(t, !turn.FirstTokenAt.Before(turn.StartedAt) && !turn.FirstTokenAt.After(turn.EndedAt))
	require.Len(t, turn.ToolCalls, 1, "the call is recorded once although it arrived twice")
	assert.Equal(t, "lookup_policy", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"topic": "returns"}, turn.ToolCalls[0].Args)
	raw, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.NotEmpty(t, raw)

	f.mu.Lock()
	assert.Equal(t, []string{"Can I return shoes?"}, f.items)
	require.Len(t, f.sessions, 1)
	assert.Equal(t, "realtime", f.sessions[0]["type"])
	assert.Equal(t, "Be brief", f.sessions[0]["instructions"])
	assert.Equal(t, []any{"text"}, f.sessions[0]["output_modalities"])
	assert.Equal(t, map[string]any{"output": map[string]any{"voice": "marin"}}, f.sessions[0]["audio"], "session_update merges over the defaults")
	assert.Equal(t, "Bearer sk-test", f.headers[0].Get("Authorization"))
	assert.Empty(t, f.headers[0].Get("OpenAI-Beta"), "the GA protocol sends no beta header")
	assert.Equal(t, "/v1/realtime?model=gpt-realtime", f.paths[0])
	f.mu.Unlock()

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "thanks", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bye now.", turn.Text)
	assert.True(t, turn.Done, "the end_session call ends the session")
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 24.0, arts.Metrics["agent_input_tokens"])
	assert.Equal(t, 14.0, arts.Metrics["agent_output_tokens"])
	assert.Greater(t, arts.Metrics["ws_messages_received"], 4.0)
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "session_created")
	assert.Contains(t, kinds, "tool_call")
	_, err = sess.Close(context.Background())
	assert.NoError(t, err)
}

func TestBetaProtocol(t *testing.T) {
	f := &fakeServer{replies: []string{"hello there"}, beta: true}
	tgt := newTarget(t, f, func(c *Config) { c.Model = "" })
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "hello there", turn.Text)
	f.mu.Lock()
	assert.Equal(t, "realtime=v1", f.headers[0].Get("OpenAI-Beta"))
	assert.Equal(t, "/v1/realtime", f.paths[0], "no model parameter without a model")
	assert.Equal(t, []any{"text"}, f.sessions[0]["modalities"])
	assert.Nil(t, f.sessions[0]["type"])
	f.mu.Unlock()
}

func TestNoSessionUpdated(t *testing.T) {
	f := &fakeServer{replies: []string{"ok"}, noUpdate: true}
	tgt := newTarget(t, f, nil)
	started := time.Now()
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err, "a server that never confirms the session is given up on, not failed")
	assert.GreaterOrEqual(t, time.Since(started), HandshakeTimeout)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", turn.Text)
}

func TestErrors(t *testing.T) {
	t.Run("error event", func(t *testing.T) {
		sess, err := newTarget(t, &fakeServer{replies: []string{"x"}, failWith: "error"}, nil).Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the fake refused")
	})
	t.Run("failed response", func(t *testing.T) {
		sess, err := newTarget(t, &fakeServer{replies: []string{"x"}, failWith: "failed"}, nil).Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model overloaded")
	})
	t.Run("turn timeout", func(t *testing.T) {
		silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			<-r.Context().Done()
			_ = conn.CloseNow()
		}))
		t.Cleanup(silent.Close)
		tgt := New("rt", Config{URL: silent.URL, Timeout: 300 * time.Millisecond})
		sess, err := tgt.Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no response.done within")
	})
	t.Run("nothing listening", func(t *testing.T) {
		_, err := New("rt", Config{URL: "ws://127.0.0.1:1/v1/realtime?model=x", Timeout: time.Second}).Start(context.Background(), runCtx(t))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connect ws://127.0.0.1:1/v1/realtime:", "the query string (with its model) is not in the error")
	})
	t.Run("audio mode without speech", func(t *testing.T) {
		_, err := New("rt", Config{Mode: ModeAudio}).Start(context.Background(), runCtx(t))
		assert.ErrorIs(t, err, ErrNoSpeech)
	})
}

func TestMerge(t *testing.T) {
	got := merge(map[string]any{"a": 1, "m": map[string]any{"x": 1, "y": 2}}, map[string]any{"b": 2, "m": map[string]any{"y": 3}})
	assert.Equal(t, map[string]any{"a": 1, "b": 2, "m": map[string]any{"x": 1, "y": 3}}, got)
}
