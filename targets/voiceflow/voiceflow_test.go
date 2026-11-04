package voiceflow

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

// runtime is a scripted Voiceflow runtime over REST (PUT /conversation/{id})
// and over socket.io (/v4/interact/socket): the launch answers a greeting
// and the replies answer the user turns in order, the last one ending the
// conversation. It records every request.
type runtime struct {
	mu       sync.Mutex
	replies  []string
	greeting string
	i        int
	rich     bool // buttons, a card, a tool call and a handoff on the first reply
	requests []recorded
	deletes  []string
	patches  []map[string]any
	// socket side
	starts   []map[string]any
	actions  []map[string]any
	rejectAt int // reject the n-th text action (1-based); 0 never
	pings    int
}

type recorded struct {
	method, path, query, auth string
	body                      map[string]any
}

func textTrace(msg string) map[string]any {
	return map[string]any{"type": "text", "time": 1, "payload": map[string]any{"message": msg, "slate": map[string]any{"content": []any{}}}}
}

// answer is the traces for an action, shared by both transports.
func (rt *runtime) answer(action map[string]any) (traces []map[string]any, last bool) {
	switch action["type"] {
	case "launch":
		if rt.greeting != "" {
			traces = append(traces, textTrace(rt.greeting))
		}
	case "text":
		i := rt.i
		rt.i++
		reply := "no more replies"
		if i < len(rt.replies) {
			reply = rt.replies[i]
		}
		traces = append(traces, map[string]any{"type": "debug", "payload": map[string]any{"message": "matched intent"}})
		if rt.rich && i == 0 {
			traces = append(traces,
				map[string]any{"type": "tool-call", "payload": map[string]any{"callID": "c1", "state": "start", "name": "lookup_order", "arguments": map[string]any{"id": "ORD-1"}}},
				map[string]any{"type": "tool-call", "payload": map[string]any{"callID": "c1", "state": "end"}},
				map[string]any{"type": "speak", "payload": map[string]any{"type": "message", "message": reply}},
				map[string]any{"type": "choice", "payload": map[string]any{"buttons": []any{map[string]any{"name": "Yes"}, map[string]any{"name": "No"}}}},
				map[string]any{"type": "cardV2", "payload": map[string]any{"title": "Mochi Runner", "description": map[string]any{"text": "Blue, 42"}}},
				map[string]any{"type": "live-agent-handoff", "payload": map[string]any{"provider": "zendesk", "message": "Connecting you", "conversationID": "x", "event": "chat_started"}},
			)
		} else {
			traces = append(traces, textTrace(reply))
		}
		last = i == len(rt.replies)-1
	}
	return traces, last
}

func (rt *runtime) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/interact/socket/", rt.socket)
	mux.HandleFunc("/", rt.rest)
	return mux
}

func (rt *runtime) rest(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.requests = append(rt.requests, recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), body: body})
	if r.Header.Get("Authorization") != "Bearer tok-1" {
		http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		rt.deletes = append(rt.deletes, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "deleted"})
		return
	case http.MethodPatch:
		rt.patches = append(rt.patches, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "updated"})
		return
	}
	action, _ := body["action"].(map[string]any)
	traces, last := rt.answer(action)
	if last {
		traces = append(traces, map[string]any{"type": "end", "payload": nil})
	}
	w.Header().Set("Content-Type", "application/json")
	// A control character the runtime may leave in a string.
	b, _ := json.Marshal(map[string]any{"traces": traces})
	_, _ = w.Write(append(b[:len(b)-1], []byte("\x01}")...))
}

// socket is the socket.io runtime: Engine.IO open, namespace connect, then
// the client.start / session.create / action.send lifecycle.
func (rt *runtime) socket(w http.ResponseWriter, r *http.Request) {
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
	texts := 0
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		s := string(data)
		switch {
		case s == "40":
			write(`40{"sid":"ns1"}`)
			write("2") // a ping the client must answer
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
				if payload["authorization"] != "VF.DM.key" {
					emit("error", map[string]any{"code": "unauthorized", "error": "invalid API key"})
					continue
				}
				emit("client.started", map[string]any{"newSessionRequired": true})
			case "session.create":
				emit("session.created", map[string]any{"sessionKey": "sess-jwt"})
			case "action.send":
				action, _ := payload["action"].(map[string]any)
				rt.mu.Lock()
				rt.actions = append(rt.actions, action)
				if action["type"] == "text" {
					texts++
				}
				reject := action["type"] == "text" && rt.rejectAt == texts
				traces, last := rt.answer(action)
				rt.mu.Unlock()
				if reject {
					emit("action.status", map[string]any{"status": "rejected", "messageID": "m", "reason": "an action is already running"})
					continue
				}
				emit("action.status", map[string]any{"status": "accepted", "messageID": "m"})
				for _, tr := range traces {
					emit("action.trace", map[string]any{"trace": tr, "messageID": "m"})
				}
				emit("action.status", map[string]any{"status": "completed", "messageID": "m"})
				if last {
					emit("session.ended", map[string]any{"reason": "end_of_diagram"})
				}
			}
		}
	}
}

func newTarget(t *testing.T, rt *runtime, transport string, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(rt.handler())
	t.Cleanup(srv.Close)
	c := Config{Transport: transport, BaseURL: srv.URL, APIKey: "{{secrets.VF}}", ProjectID: "proj-1", Environment: "staging", Version: "draft", UserID: "user-{{uuid}}", Timeout: 5 * time.Second}
	if transport == TransportWebSocket {
		c.APIKey = "{{secrets.DM}}"
	}
	if cfg != nil {
		cfg(&c)
	}
	return New("vf", c)
}

func render(s string) (string, error) {
	s = strings.ReplaceAll(s, "{{secrets.VF}}", "tok-1")
	s = strings.ReplaceAll(s, "{{secrets.DM}}", "VF.DM.key")
	s = strings.ReplaceAll(s, "{{vars.name}}", "Marta")
	return strings.ReplaceAll(s, "{{uuid}}", "0000"), nil
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: render}
}

type renderTarget struct{ target.Target }

func (r renderTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Render = render
	return r.Target.Start(ctx, run)
}

func TestConformance(t *testing.T) {
	for _, transport := range Transports {
		t.Run(transport, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				return renderTarget{newTarget(t, &runtime{replies: replies}, transport, nil)}
			}})
		})
	}
}

func TestRESTConversation(t *testing.T) {
	rt := &runtime{replies: []string{"ORD-1 ships tomorrow.", "Bye!"}, greeting: "Welcome to Mochi Shoes!", rich: true}
	tgt := newTarget(t, rt, TransportREST, func(c *Config) { c.Variables = map[string]string{"customer_name": "{{vars.name}}"}; c.Reset = true })
	ctx := context.Background()
	sess, err := tgt.Start(ctx, runCtx(t))
	require.NoError(t, err)
	rt.mu.Lock()
	require.Len(t, rt.requests, 3, "reset, variables, launch")
	del, patch, launch := rt.requests[0], rt.requests[1], rt.requests[2]
	rt.mu.Unlock()
	assert.Equal(t, http.MethodDelete, del.method)
	assert.Equal(t, "/conversation-state/user-0000", del.path)
	assert.Equal(t, "environmentAlias=staging&projectID=proj-1&version=draft", del.query)
	assert.Equal(t, http.MethodPatch, patch.method)
	assert.Equal(t, "/conversation-state/user-0000/variables", patch.path)
	assert.Equal(t, map[string]any{"variables": map[string]any{"customer_name": "Marta"}}, patch.body)
	assert.Equal(t, http.MethodPut, launch.method)
	assert.Equal(t, "/conversation/user-0000", launch.path)
	assert.Equal(t, "environmentAlias=staging&projectID=proj-1", launch.query)
	assert.Equal(t, "Bearer tok-1", launch.auth)
	assert.Equal(t, map[string]any{"action": map[string]any{"type": "launch", "payload": map[string]any{}}, "version": "draft"}, launch.body)

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Where is ORD-1?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Welcome to Mochi Shoes!\nORD-1 ships tomorrow.\n[buttons: Yes | No]\n[card: Mochi Runner: Blue, 42]", turn.Text, "the greeting is read with the first turn; buttons and cards are summarised")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup_order", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"id": "ORD-1"}, turn.ToolCalls[0].Args)
	assert.False(t, turn.Done)
	raw, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, raw, 2, "the launch traces and the turn's traces")

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "thanks, bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bye!", turn.Text)
	assert.True(t, turn.Done, "the end trace ends the session")
	traces, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Equal(t, "debug", traces[0].(map[string]any)["type"], "diagnostic traces stay in the raw payload")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5.0, arts.Metrics["http_requests"])
	assert.Greater(t, arts.Metrics["voiceflow_traces"], 5.0)
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["reset"])
	assert.Equal(t, 1, kinds["variables"])
	assert.Equal(t, 1, kinds["launch"])
	assert.Equal(t, 1, kinds["greeting"])
	assert.Equal(t, 1, kinds["tool_call"], "one start, the end is not a call")
	assert.Equal(t, 1, kinds["live_agent_handoff"])
	assert.Equal(t, 1, kinds["end"])
}

func TestWebSocketConversation(t *testing.T) {
	rt := &runtime{replies: []string{"30 days.", "Bye!"}, greeting: "Hi there!"}
	tgt := newTarget(t, rt, TransportWebSocket, nil)
	ctx := context.Background()
	sess, err := tgt.Start(ctx, runCtx(t))
	require.NoError(t, err)
	rt.mu.Lock()
	require.Len(t, rt.starts, 1)
	start := rt.starts[0]
	rt.mu.Unlock()
	assert.Equal(t, "user-0000", start["userID"])
	assert.Equal(t, "proj-1", start["projectID"])
	assert.Equal(t, "staging", start["environmentID"])
	assert.Equal(t, "VF.DM.key", start["authorization"])

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "return window?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hi there!\n30 days.", turn.Text, "the launch's greeting is read with the first turn")
	assert.False(t, turn.Done)
	rt.mu.Lock()
	require.Len(t, rt.actions, 2)
	assert.Equal(t, map[string]any{"type": "launch", "payload": map[string]any{}}, rt.actions[0])
	assert.Equal(t, map[string]any{"type": "text", "payload": "return window?"}, rt.actions[1])
	rt.mu.Unlock()

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bye!", turn.Text)
	assert.True(t, turn.Done, "session.ended ends the session")
	assert.Error(t, sess.Send(ctx, target.UserTurn{Text: "still there?"}))

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, arts.Metrics["ws_messages_received"], 6.0)
	assert.Equal(t, 5.0, arts.Metrics["ws_messages_sent"], "client.start, session.create, launch and two texts")
	kinds := map[string]int{}
	for _, e := range arts.Events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 1, kinds["client_started"])
	assert.Equal(t, 1, kinds["session_created"])
	assert.Equal(t, 1, kinds["greeting"])
	assert.Equal(t, 1, kinds["session_ended"])
	time.Sleep(20 * time.Millisecond)
	rt.mu.Lock()
	assert.Equal(t, 1, rt.pings, "the ping was answered")
	rt.mu.Unlock()
}

func TestWebSocketRejectedAction(t *testing.T) {
	rt := &runtime{replies: []string{"a", "b"}, rejectAt: 1}
	sess, err := newTarget(t, rt, TransportWebSocket, nil).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected the action: an action is already running")
}

func TestStartErrors(t *testing.T) {
	rt := &runtime{replies: []string{"x"}}
	_, err := newTarget(t, rt, TransportREST, func(c *Config) { c.APIKey = "{{secrets.OTHER}}" }).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	_, err = newTarget(t, rt, TransportREST, func(c *Config) { c.ProjectID = "" }).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project_id is required")
	_, err = newTarget(t, rt, TransportWebSocket, func(c *Config) { c.APIKey = "{{secrets.OTHER}}" }).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client.start refused: unauthorized invalid API key")
}

func TestDefaults(t *testing.T) {
	rest := New("a", Config{APIKey: "k", ProjectID: "p"}).(*Target)
	assert.Equal(t, TransportREST, rest.cfg.Transport)
	assert.Equal(t, DefaultBaseURL, rest.cfg.BaseURL)
	assert.Equal(t, "main", rest.cfg.Environment)
	assert.Equal(t, "published", rest.cfg.Version)
	ws := New("b", Config{Transport: TransportWebSocket, APIKey: "k", ProjectID: "p"}).(*Target)
	assert.Equal(t, DefaultWebSocketBaseURL, ws.cfg.BaseURL)
}
