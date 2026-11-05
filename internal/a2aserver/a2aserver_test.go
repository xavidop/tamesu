package a2aserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

func rpc(t *testing.T, url, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func userMessage(text, taskID, contextID string) map[string]any {
	return map[string]any{"message": map[string]any{"kind": "message", "messageId": "m1", "role": "user", "taskId": taskID, "contextId": contextID,
		"parts": []any{map[string]any{"kind": "text", "text": text}}}}
}

func TestScriptedAgent(t *testing.T) {
	srv, err := Start(Config{Name: "Billing", Description: "Refunds", Skills: []Skill{{ID: "refund"}, {ID: "invoice-lookup", Name: "Invoice lookup"}},
		Default: Reply{Text: "I can help with refunds and invoices."},
		Replies: []Reply{
			{When: regexp.MustCompile(`(?i)refund`), Text: "Refund of 42 euros issued, reference RF-889", Data: map[string]any{"reference": "RF-889"}},
			{When: regexp.MustCompile(`(?i)cancel`), Text: "Which order?", State: StateInputRequired},
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	resp, err := http.Get(srv.URL() + "/.well-known/agent-card.json")
	require.NoError(t, err)
	var card map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&card))
	_ = resp.Body.Close()
	assert.Equal(t, "Billing", card["name"])
	skills := card["skills"].([]any)
	require.Len(t, skills, 2)
	assert.Equal(t, "refund", skills[0].(map[string]any)["name"], "a skill without a name is named by its id")

	out := rpc(t, srv.URL()+"/a2a", "message/send", userMessage("Please refund order ORD-1", "", ""))
	res := out["result"].(map[string]any)
	st := res["status"].(map[string]any)
	assert.Equal(t, "completed", st["state"])
	parts := st["message"].(map[string]any)["parts"].([]any)
	assert.Equal(t, "Refund of 42 euros issued, reference RF-889", parts[0].(map[string]any)["text"])
	assert.Equal(t, "data", parts[1].(map[string]any)["kind"])

	out = rpc(t, srv.URL(), "message/send", userMessage("cancel it", "", res["contextId"].(string)))
	res2 := out["result"].(map[string]any)
	assert.Equal(t, "input-required", res2["status"].(map[string]any)["state"])
	assert.Equal(t, res["contextId"], res2["contextId"], "the context carries over")
	out = rpc(t, srv.URL(), "message/send", userMessage("ORD-2", res2["id"].(string), res2["contextId"].(string)))
	assert.Equal(t, res2["id"], out["result"].(map[string]any)["id"], "the task was continued")
	assert.Equal(t, "I can help with refunds and invoices.", out["result"].(map[string]any)["status"].(map[string]any)["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"])

	got := rpc(t, srv.URL(), "tasks/get", map[string]any{"id": res2["id"]})
	assert.Equal(t, res2["id"], got["result"].(map[string]any)["id"])
	canceled := rpc(t, srv.URL(), "tasks/cancel", map[string]any{"id": res2["id"]})
	assert.Equal(t, "canceled", canceled["result"].(map[string]any)["status"].(map[string]any)["state"])
	missing := rpc(t, srv.URL(), "tasks/get", map[string]any{"id": "nope"})
	assert.NotNil(t, missing["error"])

	calls := srv.Delegations()
	require.Len(t, calls, 3)
	assert.Equal(t, "Please refund order ORD-1", calls[0].Message)
	assert.Equal(t, "Refund of 42 euros issued, reference RF-889", calls[0].Reply)
	assert.Equal(t, "input-required", calls[1].State)
	assert.False(t, calls[0].Streamed)
}

func TestStreamAndFuzz(t *testing.T) {
	srv, err := Start(Config{Name: "Billing", Default: Reply{Text: "ok"}, Fuzz: &target.ToolFuzz{Mode: target.FuzzError}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "7", "method": "message/stream", "params": userMessage("refund", "", "")})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	events := strings.Count(string(raw), "data: ")
	assert.Equal(t, 2, events, "the task, then the final status")
	assert.Contains(t, string(raw), `"state":"failed"`)
	assert.Contains(t, string(raw), "the service is unavailable")
	calls := srv.Delegations()
	require.Len(t, calls, 1)
	assert.True(t, calls[0].Streamed)
	assert.Equal(t, "error", calls[0].Fuzzed)
	assert.Equal(t, "failed", calls[0].State)

	for _, tc := range []struct {
		mode string
		text string
		data bool
	}{
		{target.FuzzEmpty, "", false}, {target.FuzzHostile, target.HostileResult, false}, {target.FuzzWrongType, "42", false},
		{target.FuzzMalformed, `{"status": "ok", "items": [{"id": "ORD-7`, false}, {target.FuzzCustom, "custom text", false},
	} {
		r := fuzzed(target.ToolFuzz{Mode: tc.mode, Result: "custom text"}, Reply{Text: "normal"})
		assert.Equal(t, tc.text, r.Text, tc.mode)
	}
	assert.Equal(t, "normal", fuzzed(target.ToolFuzz{Mode: target.FuzzDelay, Delay: time.Millisecond}, Reply{Text: "normal"}).Text)
	assert.Len(t, fuzzed(target.ToolFuzz{Mode: target.FuzzHuge}, Reply{}).Text, target.HugeResultSize)
	assert.NotEmpty(t, fuzzed(target.ToolFuzz{Mode: target.FuzzCustom, Result: map[string]any{"a": 1}}, Reply{}).Data)
}

func TestForwardRecordsBothDirections(t *testing.T) {
	// The real agent: answers message/send with a completed task and
	// message/stream with two events.
	var gotAuth string
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := map[string]any{"kind": "task", "id": "t1", "contextId": "c1", "status": map[string]any{"state": "completed",
			"message": map[string]any{"kind": "message", "role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "Invoice INV-7 is paid"}}}}}
		if req["method"] == "message/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []map[string]any{
				{"kind": "task", "id": "t2", "contextId": "c1", "status": map[string]any{"state": "submitted"}},
				{"kind": "status-update", "taskId": "t2", "contextId": "c1", "final": true, "status": map[string]any{"state": "completed",
					"message": map[string]any{"kind": "message", "role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "streamed answer"}}}}},
			} {
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": ev})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": reply})
	}))
	t.Cleanup(real.Close)
	proxy, err := Start(Config{Name: "Billing", Forward: real.URL, ForwardHeaders: map[string]string{"Authorization": "Bearer real-token"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = proxy.Close() })

	out := rpc(t, proxy.URL(), "message/send", userMessage("Is invoice INV-7 paid?", "", ""))
	assert.Equal(t, "t1", out["result"].(map[string]any)["id"], "the real answer passed through")
	assert.Equal(t, "Bearer real-token", gotAuth)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "2", "method": "message/stream", "params": userMessage("stream it", "", "")})
	resp, err := http.Post(proxy.URL(), "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 2, strings.Count(string(raw), "data: "))

	calls := proxy.Delegations()
	require.Len(t, calls, 2)
	assert.True(t, calls[0].Forwarded)
	assert.Equal(t, "Is invoice INV-7 paid?", calls[0].Message)
	assert.Equal(t, "Invoice INV-7 is paid", calls[0].Reply)
	assert.Equal(t, "completed", calls[0].State)
	assert.Equal(t, "t1", calls[0].TaskID)
	assert.Equal(t, "streamed answer", calls[1].Reply)
	assert.True(t, calls[1].Streamed)
	assert.Equal(t, "t2", calls[1].TaskID)
}

func TestValidateAndErrors(t *testing.T) {
	assert.ErrorIs(t, Config{Name: "x"}.Validate(), ErrNoReply)
	assert.NoError(t, Config{Forward: "http://x"}.Validate())
	assert.NoError(t, Config{Default: Reply{Data: map[string]any{"a": 1}}}.Validate())
	_, err := Start(Config{Addr: "256.0.0.1:1"})
	assert.Error(t, err)
	srv, err := Start(Config{Default: Reply{Text: "x"}})
	require.NoError(t, err)
	resp, err := http.Post(srv.URL(), "application/json", strings.NewReader("not json"))
	require.NoError(t, err)
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	assert.NotNil(t, out["error"])
	out = rpc(t, srv.URL(), "nope/method", nil)
	assert.NotNil(t, out["error"])
	require.NoError(t, srv.Close())
	require.NoError(t, srv.Close(), "idempotent")
	_ = context.Background()
}
