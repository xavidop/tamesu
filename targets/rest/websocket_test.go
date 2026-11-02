package rest

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

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// wsAgent is a scripted websocket agent: each message it receives gets the
// next reply streamed in word chunks; the last chunk carries the markers.
// With idleOnly it never marks the turn, so the client has to rely on idle.
type wsAgent struct {
	mu       sync.Mutex
	replies  []string
	i        int
	messages []map[string]any
	auth     []string
	idleOnly bool
	// closeAfter closes the socket after this many turns (0: never).
	closeAfter int
}

func (a *wsAgent) handle(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	a.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	turns := 0
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg map[string]any
		_ = json.Unmarshal(data, &msg)
		a.mu.Lock()
		a.messages = append(a.messages, msg)
		if msg["type"] == "open" {
			a.mu.Unlock()
			continue
		}
		reply := "no more replies"
		if a.i < len(a.replies) {
			reply = a.replies[a.i]
		}
		a.i++
		last := a.i >= len(a.replies)
		a.mu.Unlock()
		for _, word := range splitWords(reply) {
			_ = conn.Write(ctx, websocket.MessageText, []byte(mustJSON(map[string]any{"delta": word})))
			time.Sleep(2 * time.Millisecond)
		}
		final := map[string]any{"tool_calls": []any{map[string]any{"name": "lookup", "args": map[string]any{"q": msg["message"]}}}}
		if !a.idleOnly {
			final["done"] = true
			final["end_session"] = last
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(mustJSON(final)))
		turns++
		if a.closeAfter > 0 && turns >= a.closeAfter {
			_ = conn.Close(websocket.StatusGoingAway, "bye")
			return
		}
	}
}

func newWSTarget(t *testing.T, a *wsAgent, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(srv.Close)
	c := Config{
		URL:           "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
		Headers:       map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"},
		Body:          map[string]any{"session_id": "{{session}}", "message": "{{message}}"},
		Open:          map[string]any{"type": "open", "session_id": "{{session}}"},
		TextPath:      "$.delta",
		ToolCallsPath: "$.tool_calls[*]",
		DonePath:      "$.end_session",
		TurnDonePath:  "$.done",
		Streaming:     StreamingWebSocket,
		SessionID:     "sess-{{uuid}}",
		Idle:          150 * time.Millisecond,
		Timeout:       5 * time.Second,
	}
	if cfg != nil {
		cfg(&c)
	}
	return New("ws", c)
}

func TestWebSocketConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
		return newWSTarget(t, &wsAgent{replies: replies}, nil)
	}})
}

func TestWebSocketTurn(t *testing.T) {
	a := &wsAgent{replies: []string{"Our window is 30 days.", "Bye"}}
	tgt := newWSTarget(t, a, nil)
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Can I return shoes?", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text)
	assert.Equal(t, http.StatusSwitchingProtocols, turn.Status)
	assert.False(t, turn.Done)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.True(t, !turn.FirstTokenAt.Before(turn.StartedAt) && !turn.FirstTokenAt.After(turn.EndedAt))
	assert.Less(t, turn.EndedAt.Sub(turn.StartedAt), 150*time.Millisecond, "the done marker ends the turn, not the idle timeout")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, map[string]any{"q": "Can I return shoes?"}, turn.ToolCalls[0].Args)
	events, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, events, 10, "nine word/space deltas and the final message")

	a.mu.Lock()
	require.Len(t, a.messages, 2, "the open message, then the turn")
	assert.Equal(t, map[string]any{"type": "open", "session_id": "sess-0000"}, a.messages[0])
	assert.Equal(t, map[string]any{"session_id": "sess-0000", "message": "Can I return shoes?"}, a.messages[1])
	assert.Equal(t, []string{"Bearer s3cret"}, a.auth, "one connection for the session, with the rendered header")
	a.mu.Unlock()

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "ok", At: time.Now()}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.True(t, turn.Done)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3.0, arts.Metrics["ws_messages_sent"])
	assert.Equal(t, 12.0, arts.Metrics["ws_messages_received"])
	assert.Error(t, sess.Send(context.Background(), target.UserTurn{Text: "after close"}))
	_, err = sess.Close(context.Background())
	assert.NoError(t, err)
}

func TestWebSocketIdleEndsTheTurn(t *testing.T) {
	a := &wsAgent{replies: []string{"streamed on idle"}, idleOnly: true}
	tgt := newWSTarget(t, a, nil)
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi", At: time.Now()}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "streamed on idle", turn.Text)
	assert.False(t, turn.Done, "no done marker: the session goes on")
	assert.GreaterOrEqual(t, turn.EndedAt.Sub(turn.FirstTokenAt), 150*time.Millisecond, "the turn ended after the idle silence")
	require.Len(t, turn.ToolCalls, 1)
}

func TestWebSocketErrors(t *testing.T) {
	t.Run("closed by the agent", func(t *testing.T) {
		a := &wsAgent{replies: []string{"one", "two"}, closeAfter: 1}
		sess, err := newWSTarget(t, a, nil).Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
		_, err = sess.Receive(context.Background())
		require.NoError(t, err)
		err = sess.Send(context.Background(), target.UserTurn{Text: "again"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})
	t.Run("no message within the timeout", func(t *testing.T) {
		silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			<-r.Context().Done()
			_ = conn.CloseNow()
		}))
		t.Cleanup(silent.Close)
		tgt := New("ws", Config{URL: silent.URL, Streaming: StreamingWebSocket, Timeout: 200 * time.Millisecond})
		sess, err := tgt.Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no message within")
	})
	t.Run("nothing listening", func(t *testing.T) {
		tgt := New("ws", Config{URL: "ws://127.0.0.1:1/ws", Streaming: StreamingWebSocket, Timeout: time.Second})
		_, err := tgt.Start(context.Background(), runCtx(t))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "websocket")
	})
	t.Run("plain text messages are the reply", func(t *testing.T) {
		echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow() //nolint:errcheck // test server
			for {
				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte("echo: "+string(data)))
			}
		}))
		t.Cleanup(echo.Close)
		tgt := New("ws", Config{URL: echo.URL, Streaming: StreamingWebSocket, Body: "{{message}}", Idle: 100 * time.Millisecond})
		sess, err := tgt.Start(context.Background(), runCtx(t))
		require.NoError(t, err)
		require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
		turn, err := sess.Receive(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "echo: hi", turn.Text)
	})
}
