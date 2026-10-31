package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// agent is a scripted HTTP agent: it answers replies in order and sets
// end_session on the last one. It records what it received.
type agent struct {
	mu       sync.Mutex
	replies  []string
	i        int
	requests []map[string]any
	auth     []string
	sse      bool
}

func (a *agent) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	a.requests = append(a.requests, body)
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	reply := "no more replies"
	if a.i < len(a.replies) {
		reply = a.replies[a.i]
	}
	a.i++
	last := a.i >= len(a.replies)
	if a.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, word := range splitWords(reply) {
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"delta": word}))
			fl.Flush()
			time.Sleep(2 * time.Millisecond)
		}
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"end_session": last, "tool_calls": []any{map[string]any{"name": "lookup", "args": map[string]any{"q": body["message"]}}}}))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"reply":       reply,
		"end_session": last,
		"tool_calls":  []any{map[string]any{"name": "lookup", "arguments": map[string]any{"q": body["message"]}}},
		"session":     body["session_id"],
	})
}

func splitWords(s string) []string {
	var out []string
	for i, w := range splitSpace(s) {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, w)
	}
	return out
}

func splitSpace(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newTarget(t *testing.T, a *agent, sse bool) (target.Target, *httptest.Server) {
	t.Helper()
	a.sse = sse
	srv := httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(srv.Close)
	cfg := Config{
		URL:           srv.URL + "/chat",
		Headers:       map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"},
		Body:          map[string]any{"session_id": "{{session}}", "message": "{{message}}", "user": "{{vars.user}}"},
		TextPath:      "$.reply",
		ToolCallsPath: "$.tool_calls[*]",
		DonePath:      "$.end_session",
		SessionID:     "sess-{{uuid}}",
	}
	if sse {
		cfg.Streaming = StreamingSSE
		cfg.TextPath = "$.delta"
	}
	return New("api", cfg), srv
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		s = replaceAll(s, "{{secrets.TOKEN}}", "s3cret")
		s = replaceAll(s, "{{vars.user}}", "qa@example.com")
		s = replaceAll(s, "{{uuid}}", "0000")
		return s, nil
	}}
}

func replaceAll(s, old, repl string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + repl + s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestConformance(t *testing.T) {
	for _, sse := range []bool{false, true} {
		name := "json"
		if sse {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				tgt, _ := newTarget(t, &agent{replies: replies}, sse)
				return tgt
			}})
		})
	}
}

func TestJSONTurn(t *testing.T) {
	a := &agent{replies: []string{"Our window is 30 days.", "Bye"}}
	tgt, _ := newTarget(t, a, false)
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return shoes?", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text)
	assert.Equal(t, 200, turn.Status)
	assert.False(t, turn.Done)
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"q": "Can I return shoes?"}, turn.ToolCalls[0].Args)
	raw, ok := turn.Raw.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sess-0000", raw["session"], "the session id template was rendered once per session")
	// The request carried the rendered body and the secret header.
	assert.Equal(t, map[string]any{"session_id": "sess-0000", "message": "Can I return shoes?", "user": "qa@example.com"}, a.requests[0])
	assert.Equal(t, "Bearer s3cret", a.auth[0])

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "ok", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.True(t, turn.Done)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["http_requests"])
	assert.Error(t, sess.Send(context.Background(), target.UserTurn{Text: "after close"}))
}

func TestSSETurn(t *testing.T) {
	a := &agent{replies: []string{"streamed reply here"}}
	tgt, _ := newTarget(t, a, true)
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "streamed reply here", turn.Text)
	assert.True(t, turn.Done)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.True(t, !turn.FirstTokenAt.Before(turn.StartedAt) && !turn.FirstTokenAt.After(turn.EndedAt))
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, map[string]any{"q": "hi"}, turn.ToolCalls[0].Args)
	events, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, events, 6, "five word/space deltas and the final event; [DONE] is not an event")
}

func TestNonJSONBodyAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "upstream down")
	}))
	t.Cleanup(srv.Close)
	tgt := New("api", Config{URL: srv.URL, TextPath: "$.reply"})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 502, turn.Status)
	assert.Equal(t, "upstream down", turn.Text)

	_, err = New("api", Config{URL: srv.URL, TextPath: "$["}).Start(context.Background(), runCtx(t))
	assert.ErrorContains(t, err, "response.text")

	down := New("api", Config{URL: "http://127.0.0.1:1/chat", Timeout: time.Second})
	sess, err = down.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	assert.Error(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
}

func TestDefaultBody(t *testing.T) {
	a := &agent{replies: []string{"ok"}}
	srv := httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(srv.Close)
	tgt := New("api", Config{URL: srv.URL, TextPath: "$.reply", SessionID: "fixed"})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	_, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"message": "hello", "session_id": "fixed"}, a.requests[0])
}
