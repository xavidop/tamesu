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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// runtime is a scripted Voiceflow runtime, REST (PUT /conversation/{id})
// or legacy (POST /state/user/{id}/interact): the launch answers a greeting
// and the replies answer the user turns in order, the last one with an end
// trace. It records every request.
type runtime struct {
	mu       sync.Mutex
	replies  []string
	greeting string
	i        int
	legacy   bool
	rich     bool // buttons, a card, a tool call and a handoff on the first reply
	requests []recorded
	deletes  []string
	patches  []map[string]any
}

type recorded struct {
	method, path, query, auth, version string
	body                               map[string]any
}

func (rt *runtime) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.requests = append(rt.requests, recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), version: r.Header.Get("versionID"), body: body})
	if r.Header.Get("Authorization") != "Bearer tok-1" && r.Header.Get("Authorization") != "VF.DM.key" {
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
	var traces []map[string]any
	text := func(msg string) map[string]any {
		return map[string]any{"type": "text", "time": 1, "payload": map[string]any{"message": msg, "slate": map[string]any{"content": []any{}}}}
	}
	switch action["type"] {
	case "launch":
		if rt.greeting != "" {
			traces = append(traces, text(rt.greeting))
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
			traces = append(traces, text(reply))
		}
		if i == len(rt.replies)-1 {
			traces = append(traces, map[string]any{"type": "end", "payload": nil})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if rt.legacy {
		_ = json.NewEncoder(w).Encode(traces)
		return
	}
	// A control character the runtime may leave in a string.
	b, _ := json.Marshal(map[string]any{"traces": traces})
	_, _ = w.Write(append(b[:len(b)-1], []byte("\x01}")...))
}

func newTarget(t *testing.T, rt *runtime, cfg func(*Config)) target.Target {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(rt.handle))
	t.Cleanup(srv.Close)
	c := Config{BaseURL: srv.URL, APIKey: "{{secrets.VF}}", ProjectID: "proj-1", Environment: "staging", Version: "draft", UserID: "user-{{uuid}}", Timeout: 5 * time.Second}
	if rt.legacy {
		c = Config{BaseURL: srv.URL, APIKey: "{{secrets.DM}}", VersionID: "production", UserID: "user-{{uuid}}", Timeout: 5 * time.Second}
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
	for _, legacy := range []bool{false, true} {
		name := "rest"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
				return renderTarget{newTarget(t, &runtime{replies: replies, legacy: legacy}, nil)}
			}})
		})
	}
}

func TestRESTConversation(t *testing.T) {
	rt := &runtime{replies: []string{"ORD-1 ships tomorrow.", "Bye!"}, greeting: "Welcome to Mochi Shoes!", rich: true}
	tgt := newTarget(t, rt, func(c *Config) { c.Variables = map[string]string{"customer_name": "{{vars.name}}"}; c.Reset = true })
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
	assert.Equal(t, map[string]any{"action": map[string]any{"type": "launch"}, "version": "draft"}, launch.body)

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "Where is ORD-1?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Welcome to Mochi Shoes!\nORD-1 ships tomorrow.\n[buttons: Yes | No]\n[card: Mochi Runner: Blue, 42]", turn.Text, "the greeting is read with the first turn; buttons and cards are summarised")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup_order", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"id": "ORD-1"}, turn.ToolCalls[0].Args)
	assert.False(t, turn.Done)
	assert.Equal(t, 200, turn.Status)
	raw, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, raw, 2, "the launch traces and the turn's traces")
	rt.mu.Lock()
	sent := rt.requests[3]
	rt.mu.Unlock()
	assert.Equal(t, map[string]any{"action": map[string]any{"type": "text", "payload": "Where is ORD-1?"}, "version": "draft"}, sent.body)

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

func TestLegacyDialogManager(t *testing.T) {
	rt := &runtime{replies: []string{"30 days."}, legacy: true}
	tgt := newTarget(t, rt, func(c *Config) { c.Variables = map[string]string{"tier": "gold"} })
	ctx := context.Background()
	sess, err := tgt.Start(ctx, runCtx(t))
	require.NoError(t, err)
	rt.mu.Lock()
	launch := rt.requests[0]
	rt.mu.Unlock()
	assert.Equal(t, http.MethodPost, launch.method)
	assert.Equal(t, "/state/user/user-0000/interact", launch.path)
	assert.Equal(t, "VF.DM.key", launch.auth, "the Dialog API key goes bare")
	assert.Equal(t, "production", launch.version)
	assert.Equal(t, map[string]any{"action": map[string]any{"type": "launch"}, "config": map[string]any{"tts": false, "stripSSML": true}, "state": map[string]any{"variables": map[string]any{"tier": "gold"}}}, launch.body)
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "return window?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "30 days.", turn.Text)
	assert.True(t, turn.Done)
}

func TestStartErrors(t *testing.T) {
	rt := &runtime{replies: []string{"x"}}
	_, err := newTarget(t, rt, func(c *Config) { c.APIKey = "{{secrets.OTHER}}" }).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
	_, err = newTarget(t, rt, func(c *Config) { c.ProjectID = ""; c.VersionID = "" }).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project_id (REST API) or version_id")
}

func TestDefaults(t *testing.T) {
	rest := New("a", Config{APIKey: "k", ProjectID: "p"}).(*Target)
	assert.Equal(t, DefaultBaseURL, rest.cfg.BaseURL)
	assert.Equal(t, "main", rest.cfg.Environment)
	assert.Equal(t, "published", rest.cfg.Version)
	assert.False(t, rest.Legacy())
	legacy := New("b", Config{APIKey: "k", VersionID: "development"}).(*Target)
	assert.Equal(t, DefaultLegacyBaseURL, legacy.cfg.BaseURL)
	assert.True(t, legacy.Legacy())
}
