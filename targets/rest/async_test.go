package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// channel is a Direct Line-like service: conversations are opened with a
// POST, user messages are posted, and the bot's activities are read back
// with a watermark. Every user message gets a bot answer, in one or two
// activities.
type channel struct {
	mu            sync.Mutex
	conversations map[string][]map[string]any
	opened        int
	auth          []string
	split         bool
}

func (c *channel) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /conversations", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.opened++
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		id := fmt.Sprintf("conv-%d", c.opened)
		c.conversations[id] = []map[string]any{{"from": "bot", "text": "Welcome!"}}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"conversationId": id, "token": "tok-" + id})
	})
	mux.HandleFunc("POST /conversations/{id}/activities", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		id := r.PathValue("id")
		if _, ok := c.conversations[id]; !ok {
			http.Error(w, "no such conversation", http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		text, _ := body["text"].(string)
		c.conversations[id] = append(c.conversations[id], map[string]any{"from": "user", "text": text})
		reply := "You said: " + text
		if c.split {
			c.conversations[id] = append(c.conversations[id], map[string]any{"from": "bot", "text": "One moment. "})
		}
		c.conversations[id] = append(c.conversations[id], map[string]any{"from": "bot", "text": reply, "end": text == "bye"})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("%s|%d", id, len(c.conversations[id]))})
	})
	mux.HandleFunc("GET /conversations/{id}/activities", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		id := r.PathValue("id")
		acts := c.conversations[id]
		from := 0
		if wm := r.URL.Query().Get("watermark"); wm != "" {
			_, _ = fmt.Sscanf(wm, "%d", &from)
		}
		if from > len(acts) {
			from = len(acts)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"activities": acts[from:], "watermark": fmt.Sprint(len(acts))})
	})
	return mux
}

func newChannelTarget(t *testing.T, c *channel, poll Poll) (target.Target, *httptest.Server) {
	t.Helper()
	if c.conversations == nil {
		c.conversations = map[string][]map[string]any{}
	}
	srv := httptest.NewServer(c.handler())
	t.Cleanup(srv.Close)
	poll.URL = srv.URL + "/conversations/{{session.conversation}}/activities?watermark={{cursor}}"
	poll.Headers = map[string]string{"Authorization": "Bearer {{session.token}}"}
	if poll.Cursor == "" {
		poll.Cursor = "$.watermark"
	}
	cfg := Config{
		URL:     srv.URL + "/conversations/{{session.conversation}}/activities",
		Headers: map[string]string{"Authorization": "Bearer {{session.token}}"},
		Body:    map[string]any{"type": "message", "text": "{{message}}", "from": "{{session}}"},
		SessionOpen: &Request{
			URL:     srv.URL + "/conversations",
			Headers: map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"},
			Capture: map[string]string{"conversation": "$.conversationId", "token": "$.token"},
		},
		SentCapture: map[string]string{"activity": "$.id"},
		TextPath:    "$.activities[?(@.from == 'bot')].text",
		DonePath:    "$.activities[?(@.end == true)].end",
		Poll:        &poll,
		Timeout:     5 * time.Second,
	}
	return New("channel", cfg), srv
}

func TestSessionOpenAndPoll(t *testing.T) {
	c := &channel{}
	tgt, _ := newChannelTarget(t, c, Poll{Interval: 10 * time.Millisecond})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer s3cret"}, c.auth, "session.open rendered the secret")

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	// The greeting posted before the turn is read with it, the cursor moves on.
	assert.Equal(t, "Welcome!You said: hello", turn.Text)
	assert.Equal(t, 200, turn.Status)
	assert.False(t, turn.Done)
	assert.False(t, turn.FirstTokenAt.IsZero())

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "bye"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "You said: bye", turn.Text, "only the activities after the watermark")
	assert.True(t, turn.Done)
	s := sess.(*session)
	assert.Equal(t, "conv-1|5", s.values["sent.activity"], "request.capture took the posted activity id")

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["http_requests"])
	assert.GreaterOrEqual(t, arts.Metrics["poll_requests"], 2.0)
}

func TestPollIdleAccumulates(t *testing.T) {
	c := &channel{split: true}
	tgt, _ := newChannelTarget(t, c, Poll{Interval: 10 * time.Millisecond, Idle: 100 * time.Millisecond})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Welcome!One moment. You said: hello", turn.Text)
}

func TestPollWithoutCursorTakesTheLatestState(t *testing.T) {
	// A history endpoint that returns the whole thread every time: the reply
	// grows while the bot is still typing, and the turn keeps the last state.
	var mu sync.Mutex
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ts": "1.0"})
	})
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		assert.Equal(t, "1.0", r.URL.Query().Get("oldest"), "the poll used the sent capture")
		msgs := []any{map[string]any{"user": "U1", "text": "hello"}}
		if polls >= 2 {
			msgs = append(msgs, map[string]any{"bot_id": "B1", "text": "Hi"})
		}
		if polls >= 3 {
			msgs = append(msgs, map[string]any{"bot_id": "B1", "text": " there"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": msgs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := Config{
		URL:         srv.URL + "/send",
		SentCapture: map[string]string{"ts": "$.ts"},
		TextPath:    "$.messages[?(@.bot_id)].text",
		Poll:        &Poll{Request: Request{URL: srv.URL + "/history?oldest={{sent.ts}}"}, Interval: 10 * time.Millisecond, Idle: 80 * time.Millisecond},
		Timeout:     5 * time.Second,
	}
	sess, err := New("slack", cfg).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Hi there", turn.Text)
	raw, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, raw, 1, "without a cursor the latest poll replaces the last")
}

func TestPollTimesOutWithoutAReply(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("GET /history", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"messages": []}`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL + "/send", TextPath: "$.messages[*].text",
		Poll: &Poll{Request: Request{URL: srv.URL + "/history"}, Interval: 5 * time.Millisecond, Timeout: 60 * time.Millisecond}}
	sess, err := New("slow", cfg).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hello"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reply within 60ms")
}

func TestSessionOpenFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad token", http.StatusUnauthorized) }))
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL + "/send", SessionOpen: &Request{URL: srv.URL + "/open"}, Poll: &Poll{Request: Request{URL: srv.URL + "/h"}}}
	_, err := New("x", cfg).Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session.open: HTTP 401: bad token")
}

func TestRefusedRequestIsTheTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL + "/send", TextPath: "$.text", Poll: &Poll{Request: Request{URL: srv.URL + "/h"}}}
	sess, err := New("x", cfg).Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 429, turn.Status)
	assert.Equal(t, "rate limited", turn.Text)
}

// bot is a Telegram-like bot: it receives updates on its webhook and answers
// through the Bot API base URL it was configured with (tamesu's listener),
// as a form like most Bot API libraries do. A greeting goes out on /start.
type bot struct {
	api    string
	client *http.Client
}

func (b *bot) handle(w http.ResponseWriter, r *http.Request) {
	var update struct {
		Message struct {
			Chat struct {
				ID any `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&update)
	chat := fmt.Sprint(update.Message.Chat.ID)
	// Typing first (no content), then the answer.
	_, _ = b.client.PostForm(b.api+"/bot123:abc/sendChatAction", url.Values{"chat_id": {chat}, "action": {"typing"}})
	text := "Echo: " + update.Message.Text
	if update.Message.Text == "/start" {
		text = "Hello! How can I help?"
	}
	resp, err := b.client.PostForm(b.api+"/bot123:abc/sendMessage", url.Values{"chat_id": {chat}, "text": {text}})
	if err == nil {
		_ = resp.Body.Close()
	}
	w.WriteHeader(http.StatusOK)
}

func TestListen(t *testing.T) {
	b := &bot{client: &http.Client{Timeout: 5 * time.Second}}
	hook := httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(hook.Close)
	cfg := Config{
		URL:      hook.URL + "/webhook",
		Body:     map[string]any{"update_id": 1, "message": map[string]any{"chat": map[string]any{"id": "{{session}}"}, "text": "{{message}}"}},
		TextPath: "$.text",
		Listen: &Listen{
			Path:    "POST /bot*/sendMessage",
			Session: "$.chat_id",
			Reply:   map[string]any{"ok": true, "result": map[string]any{"message_id": 1, "chat": map[string]any{"id": "{{session}}"}}},
			Timeout: 5 * time.Second,
		},
		SessionID: "chat-{{uuid}}",
		Timeout:   5 * time.Second,
	}
	tgt := New("tg", cfg).(*Target)
	t.Cleanup(func() { _ = tgt.Shutdown(context.Background()) })
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	b.api = tgt.ListenURL()
	require.NotEmpty(t, b.api)
	assert.Equal(t, b.api, sess.(*session).values["listen.url"])

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "/start"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Hello! How can I help?", turn.Text)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Echo: hi", turn.Text)
	raw, ok := turn.Raw.([]any)
	require.True(t, ok)
	require.Len(t, raw, 1, "sendChatAction did not match the path, so only sendMessage was recorded")
	assert.Equal(t, "chat-0000", raw[0].(map[string]any)["chat_id"])

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["webhooks_received"], "one sendMessage per turn")
}

func TestListenRoutesBySession(t *testing.T) {
	// Two sessions share the listener: a reply reaches the session whose id
	// it names, and requests without content (typing) do not complete a turn.
	var sendTo func(chat, text string)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		chat, _ := body["chat"].(string)
		go func() {
			sendTo(chat, "")
			time.Sleep(20 * time.Millisecond)
			sendTo(chat, "reply for "+chat)
		}()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)
	n := 0
	cfg := Config{
		URL:       hook.URL + "/hook",
		Body:      map[string]any{"chat": "{{session}}", "text": "{{message}}"},
		TextPath:  "$.text",
		Listen:    &Listen{Session: "$.chat_id", Idle: 50 * time.Millisecond},
		SessionID: "{{uuid}}",
		Timeout:   5 * time.Second,
	}
	tgt := New("x", cfg).(*Target)
	t.Cleanup(func() { _ = tgt.Shutdown(context.Background()) })
	start := func(id string) target.Session {
		n++
		run := &target.RunContext{TestName: id, Dir: t.TempDir(), Render: func(s string) (string, error) { return strings.ReplaceAll(s, "{{uuid}}", id), nil }}
		sess, err := tgt.Start(context.Background(), run)
		require.NoError(t, err)
		return sess
	}
	a, b := start("A"), start("B")
	sendTo = func(chat, text string) {
		body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": text})
		resp, err := http.Post(tgt.ListenURL()+"/send", "application/json", strings.NewReader(string(body)))
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	require.NoError(t, b.Send(context.Background(), target.UserTurn{Text: "hi"}))
	require.NoError(t, a.Send(context.Background(), target.UserTurn{Text: "hi"}))
	ta, err := a.Receive(context.Background())
	require.NoError(t, err)
	tb, err := b.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "reply for A", ta.Text)
	assert.Equal(t, "reply for B", tb.Text)
	raw := ta.Raw.([]any)
	assert.Len(t, raw, 2, "the typing request is recorded with the turn")
	_, _ = a.Close(context.Background())
	_, _ = b.Close(context.Background())
}

func TestListenTimesOut(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(hook.Close)
	cfg := Config{URL: hook.URL, TextPath: "$.text", Listen: &Listen{Timeout: 50 * time.Millisecond}}
	tgt := New("x", cfg).(*Target)
	t.Cleanup(func() { _ = tgt.Shutdown(context.Background()) })
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no request from the agent within 50ms")
}

func TestSplitPattern(t *testing.T) {
	for _, tc := range []struct{ in, method, pattern string }{
		{"", "", "/"}, {"/", "", "/"}, {"POST /bot*/sendMessage", "POST", "/bot*/sendMessage"}, {"/v1/", "", "/v1/"}, {"get /x", "GET", "/x"},
	} {
		m, p := splitPattern(tc.in)
		assert.Equal(t, tc.method, m, tc.in)
		assert.Equal(t, tc.pattern, p, tc.in)
	}
}

func TestParseBody(t *testing.T) {
	assert.Equal(t, map[string]any{"a": "1", "b": []any{"x", "y"}}, parseBody([]byte("a=1&b=x&b=y"), "application/x-www-form-urlencoded"))
	assert.Equal(t, map[string]any{"a": int64(1)}, parseBody([]byte(`{"a":1}`), "application/json"))
	assert.Equal(t, "plain", parseBody([]byte("plain"), "text/plain"))
	assert.Nil(t, parseBody(nil, ""))
}
