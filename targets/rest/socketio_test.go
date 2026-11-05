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
	"github.com/ohler55/ojg/jp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// sioRuntime is a socket.io server with a Voiceflow-like lifecycle:
// client.start → client.started, session.create → session.created, then
// action.send → action.status accepted, action.trace per reply word,
// action.status completed; the last reply ends with session.ended.
type sioRuntime struct {
	mu      sync.Mutex
	replies []string
	i       int
	starts  []map[string]any
	actions []map[string]any
	pings   int
	headers []http.Header
}

func (rt *sioRuntime) handle(w http.ResponseWriter, r *http.Request) {
	rt.mu.Lock()
	rt.headers = append(rt.headers, r.Header.Clone())
	rt.mu.Unlock()
	if r.URL.Query().Get("EIO") != "4" || r.URL.Query().Get("transport") != "websocket" {
		http.Error(w, "bad engine.io request", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // test server
	ctx := r.Context()
	write := func(s string) { _ = conn.Write(ctx, websocket.MessageText, []byte(s)) }
	emit := func(name string, payload any) {
		b, _ := json.Marshal([]any{name, payload})
		write("42" + string(b))
	}
	write(`0{"sid":"abc","upgrades":[],"pingInterval":25000,"pingTimeout":20000}`)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		s := string(data)
		switch {
		case s == "40":
			write(`40{"sid":"ns1"}`)
			write("2")
		case s == "3":
			rt.mu.Lock()
			rt.pings++
			rt.mu.Unlock()
		case strings.HasPrefix(s, "42"):
			var arr []json.RawMessage
			if json.Unmarshal(data[2:], &arr) != nil || len(arr) < 1 {
				continue
			}
			var name string
			_ = json.Unmarshal(arr[0], &name)
			var payload map[string]any
			if len(arr) > 1 {
				_ = json.Unmarshal(arr[1], &payload)
			}
			switch name {
			case "client.start":
				rt.mu.Lock()
				rt.starts = append(rt.starts, payload)
				rt.mu.Unlock()
				emit("client.started", map[string]any{"newSessionRequired": true})
			case "session.create":
				emit("session.created", map[string]any{"sessionKey": "sess-jwt-1"})
			case "action.send":
				action, _ := payload["action"].(map[string]any)
				rt.mu.Lock()
				rt.actions = append(rt.actions, action)
				reply := "no more replies"
				last := false
				if action["type"] == "text" {
					if rt.i < len(rt.replies) {
						reply = rt.replies[rt.i]
					}
					rt.i++
					last = rt.i >= len(rt.replies)
				} else {
					reply = "Welcome!"
				}
				rt.mu.Unlock()
				emit("action.status", map[string]any{"status": "accepted"})
				for _, word := range splitWords(reply) {
					emit("action.trace", map[string]any{"trace": map[string]any{"type": "text", "payload": map[string]any{"message": word}}})
				}
				emit("action.status", map[string]any{"status": "completed"})
				if last {
					emit("session.ended", map[string]any{"reason": "end_of_diagram"})
				}
			}
		}
	}
}

func newSocketIOTarget(t *testing.T, rt *sioRuntime) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(rt.handle))
	t.Cleanup(srv.Close)
	return New("vf", Config{
		URL:       srv.URL + "/v4/interact/socket",
		Headers:   map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"},
		Streaming: StreamingWebSocket,
		Protocol:  ProtocolSocketIO,
		Handshake: []HandshakeStep{
			{Send: map[string]any{"event": "client.start", "data": map[string]any{"userID": "{{session}}", "projectID": "proj-1", "authorization": "{{secrets.TOKEN}}"}},
				Wait: "$[?(@.event=='client.started')].event", Capture: map[string]string{"new_session": "$[?(@.event=='client.started')].data.newSessionRequired"}},
			{Send: map[string]any{"event": "session.create", "data": map[string]any{}},
				Wait: "$[?(@.event=='session.created')].event", Capture: map[string]string{"key": "$[?(@.event=='session.created')].data.sessionKey"}},
			{Send: map[string]any{"event": "action.send", "data": map[string]any{"action": map[string]any{"type": "launch"}}},
				Wait: "$[?(@.event=='action.status' && @.data.status=='completed')].event"},
		},
		Body:         map[string]any{"event": "action.send", "data": map[string]any{"action": map[string]any{"type": "text", "payload": "{{message}}"}, "key": "{{session.key}}"}},
		TextPath:     "$[?(@.event=='action.trace')].data.trace.payload.message",
		TurnDonePath: "$[?(@.event=='action.status' && @.data.status=='completed')].event",
		DonePath:     "$[?(@.event=='session.ended')].event",
		SessionID:    "user-{{uuid}}",
		Timeout:      5 * time.Second,
		Idle:         2 * time.Second,
	})
}

func TestSocketIOHandshakeAndTurns(t *testing.T) {
	rt := &sioRuntime{replies: []string{"30 days.", "Bye!"}}
	sess, err := newSocketIOTarget(t, rt).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	s := sess.(*session)
	assert.Equal(t, "true", s.values["session.new_session"], "handshake captures")
	assert.Equal(t, "sess-jwt-1", s.values["session.key"])
	rt.mu.Lock()
	require.Len(t, rt.starts, 1)
	assert.Equal(t, "user-0000", rt.starts[0]["userID"])
	assert.Equal(t, "s3cret", rt.starts[0]["authorization"], "handshake templates render")
	assert.Equal(t, "Bearer s3cret", rt.headers[0].Get("Authorization"))
	rt.mu.Unlock()

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "return window?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Welcome!30 days.", turn.Text, "the launch's traces, read during the handshake, come with the first turn")
	assert.False(t, turn.Done)
	assert.Equal(t, 101, turn.Status)
	rt.mu.Lock()
	last := rt.actions[len(rt.actions)-1]
	rt.mu.Unlock()
	assert.Equal(t, map[string]any{"type": "text", "payload": "return window?"}, last)

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bye!", turn.Text)
	assert.True(t, turn.Done, "session.ended matched response.done")
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 5.0, arts.Metrics["ws_messages_sent"], "client.start, session.create, launch, two texts")
	time.Sleep(20 * time.Millisecond)
	rt.mu.Lock()
	assert.Equal(t, 1, rt.pings, "the Engine.IO ping was answered")
	rt.mu.Unlock()
}

func TestSocketIOHandshakeTimesOut(t *testing.T) {
	rt := &sioRuntime{replies: []string{"x"}}
	tgt := newSocketIOTarget(t, rt).(*Target)
	tgt.cfg.Handshake = []HandshakeStep{{Send: map[string]any{"event": "client.start", "data": map[string]any{}}, Wait: "$[?(@.event=='never')].event", Timeout: 100 * time.Millisecond}}
	_, err := tgt.Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handshake[0]: waiting for")
}

func TestSocketIOMessageShape(t *testing.T) {
	rt := &sioRuntime{replies: []string{"x"}}
	tgt := newSocketIOTarget(t, rt).(*Target)
	tgt.cfg.Handshake = nil
	tgt.cfg.Body = map[string]any{"type": "text", "payload": "{{message}}"}
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a socket.io message is {event: name, data: payload}")
}

func TestJoinAndFilterPaths(t *testing.T) {
	// A JSON reply with several messages: join puts a newline between them,
	// and a filter path on an object payload selects the payload itself.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"traces":[{"type":"text","payload":{"message":"Hello"}},{"type":"debug","payload":{"message":"matched"}},{"type":"text","payload":{"message":"How can I help?"}},{"type":"end"}]}`))
	}))
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL, TextPath: "$.traces[?(@.type=='text')].payload.message", Join: "\n", DonePath: "$.traces[?(@.type=='end')].type"}
	sess, err := New("vf", cfg).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Hello\nHow can I help?", turn.Text)
	assert.True(t, turn.Done)

	obj := map[string]any{"event": "action.trace", "data": map[string]any{"text": "x"}}
	x, _ := jp.ParseString("$[?(@.event=='action.trace')].data.text")
	assert.Equal(t, []any{"x"}, evalPath(x, "$[?(@.event=='action.trace')].data.text", obj), "a filter path on an object wraps it")
	list := []any{map[string]any{"event": "a", "data": map[string]any{"text": "y"}}}
	x2, _ := jp.ParseString("$[?(@.event=='a')].data.text")
	assert.Equal(t, []any{"y"}, evalPath(x2, "$[?(@.event=='a')].data.text", list), "a filter path on a list runs as is")
	assert.Nil(t, firstOfPath(x, "$[?(@.event=='action.trace')].data.text", map[string]any{"event": "other"}))
}
