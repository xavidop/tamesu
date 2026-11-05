package a2aserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushSink receives push notifications.
type pushSink struct {
	mu     sync.Mutex
	bodies []map[string]any
	tokens []string
	srv    *httptest.Server
}

func newPushSink(t *testing.T) *pushSink {
	p := &pushSink{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.tokens = append(p.tokens, r.Header.Get("X-A2A-Notification-Token"))
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pushSink) wait(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := len(p.bodies)
		p.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	require.GreaterOrEqual(t, len(p.bodies), n, "push notifications received")
	return append([]map[string]any(nil), p.bodies...)
}

func TestPushNotifications(t *testing.T) {
	sink := newPushSink(t)
	srv, err := Start(Config{Name: "Billing", Default: Reply{Text: "Refund issued"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	// A send with a push config comes back submitted and finishes by push.
	params := userMessage("refund ORD-1", "", "")
	params["configuration"] = map[string]any{"blocking": false, "pushNotificationConfig": map[string]any{"url": sink.srv.URL + "/hook", "token": "tok-1"}}
	out := rpc(t, srv.URL(), "message/send", params)
	res := out["result"].(map[string]any)
	assert.Equal(t, "submitted", res["status"].(map[string]any)["state"])
	taskID := res["id"].(string)
	pushed := sink.wait(t, 1)
	assert.Equal(t, "completed", pushed[0]["status"].(map[string]any)["state"])
	assert.Equal(t, taskID, pushed[0]["id"])
	assert.Equal(t, "tok-1", sink.tokens[0])
	calls := srv.Delegations()
	require.Len(t, calls, 1)
	assert.True(t, calls[0].Pushed)
	assert.Equal(t, "Refund issued", calls[0].Reply)
	got := rpc(t, srv.URL(), "tasks/get", map[string]any{"id": taskID})
	assert.Equal(t, "completed", got["result"].(map[string]any)["status"].(map[string]any)["state"], "tasks/get sees the finished task")

	// The config methods.
	set := rpc(t, srv.URL(), "tasks/pushNotificationConfig/set", map[string]any{"taskId": taskID, "pushNotificationConfig": map[string]any{"id": "c2", "url": sink.srv.URL + "/second", "token": "tok-2"}})
	assert.Equal(t, "c2", set["result"].(map[string]any)["pushNotificationConfig"].(map[string]any)["id"])
	list := rpc(t, srv.URL(), "tasks/pushNotificationConfig/list", map[string]any{"id": taskID})
	assert.Len(t, list["result"].([]any), 2, "the config of the send and the one set")
	one := rpc(t, srv.URL(), "tasks/pushNotificationConfig/get", map[string]any{"id": taskID, "pushNotificationConfigId": "c2"})
	assert.Equal(t, sink.srv.URL+"/second", one["result"].(map[string]any)["pushNotificationConfig"].(map[string]any)["url"])
	del := rpc(t, srv.URL(), "tasks/pushNotificationConfig/delete", map[string]any{"id": taskID, "pushNotificationConfigId": "c2"})
	assert.Nil(t, del["result"])
	list = rpc(t, srv.URL(), "tasks/pushNotificationConfig/list", map[string]any{"id": taskID})
	assert.Len(t, list["result"].([]any), 1)
	missing := rpc(t, srv.URL(), "tasks/pushNotificationConfig/get", map[string]any{"id": "nope"})
	assert.EqualValues(t, CodeTaskNotFound, missing["error"].(map[string]any)["code"])
}

func TestResubscribeListAndExtendedCard(t *testing.T) {
	srv, err := Start(Config{Name: "Billing", Description: "Refunds", Skills: []Skill{{ID: "refund"}}, Default: Reply{Text: "ok"},
		Extended: &Extended{Description: "Refunds and exports for partners", Skills: []Skill{{ID: "invoice-export"}}}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	res := rpc(t, srv.URL(), "message/send", userMessage("hi", "", ""))["result"].(map[string]any)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "9", "method": "tasks/resubscribe", "params": map[string]any{"id": res["id"]}})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, 2, strings.Count(string(raw), "data: "), "the task, then the final status")
	assert.Contains(t, string(raw), `"final":true`)
	body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "9", "method": "tasks/resubscribe", "params": map[string]any{"id": "nope"}})
	resp, err = http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Contains(t, string(raw), "task not found")

	list := rpc(t, srv.URL(), "tasks/list", nil)
	assert.Len(t, list["result"].(map[string]any)["tasks"].([]any), 1)

	// The public card points at the extended one; the extended one has more.
	cardResp, err := http.Get(srv.URL() + "/.well-known/agent-card.json")
	require.NoError(t, err)
	var card map[string]any
	require.NoError(t, json.NewDecoder(cardResp.Body).Decode(&card))
	_ = cardResp.Body.Close()
	assert.Equal(t, true, card["supportsAuthenticatedExtendedCard"])
	assert.Equal(t, "JSONRPC", card["preferredTransport"])
	assert.Equal(t, true, card["capabilities"].(map[string]any)["pushNotifications"])
	assert.Len(t, card["skills"], 1)
	assert.Len(t, card["additionalInterfaces"], 1, "JSON-RPC only without a binding")
	ext := rpc(t, srv.URL(), "agent/getAuthenticatedExtendedCard", nil)["result"].(map[string]any)
	assert.Len(t, ext["skills"], 2)
	assert.Equal(t, "Refunds and exports for partners", ext["description"])
	assert.Contains(t, Methods(), "tasks/resubscribe")
}

func TestBearerToken(t *testing.T) {
	srv, err := Start(Config{Name: "Billing", Default: Reply{Text: "ok"}, Token: "s3cret"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": "message/send", "params": userMessage("hi", "", "")})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, srv.Delegations(), "an unauthorized message is not a delegation")

	req, _ := http.NewRequest(http.MethodPost, srv.URL(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, srv.Delegations(), 1)

	cardResp, err := http.Get(srv.URL() + "/.well-known/agent-card.json")
	require.NoError(t, err)
	var card map[string]any
	require.NoError(t, json.NewDecoder(cardResp.Body).Decode(&card))
	_ = cardResp.Body.Close()
	assert.NotNil(t, card["securitySchemes"], "the card is public and declares the scheme")
	assert.NotEmpty(t, card["security"])
	assert.True(t, srv.Authorized("Bearer s3cret"))
	assert.False(t, srv.Authorized("Bearer nope"))
	assert.False(t, srv.Authorized("s3cret"))
}
