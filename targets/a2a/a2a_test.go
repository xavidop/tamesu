package a2a

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// reply is what the fake agent does with one user message.
type reply struct {
	text  string
	state string // completed by default
	// artifact puts the text in an artifact instead of the status message.
	artifact bool
	// working answers a working task first; tasks/get then completes it.
	working bool
	// drop ends the stream after the first word, without a final event;
	// tasks/resubscribe then replays the rest.
	drop bool
}

// agent is a scripted A2A server: an agent card, message/send, message/stream,
// tasks/get and tasks/cancel. It records the messages it received.
type agent struct {
	mu        sync.Mutex
	replies   []reply
	i         int
	received  []message
	tasks     map[string]*task
	streaming bool
	noCard    bool
	cardPath  string
	cancelled []string
	polls     int
	rpcError  *rpcError
	// extended adds a skill to the authenticated extended card; secured puts a
	// security requirement on the card.
	extended      bool
	secured       bool
	extendedCalls int
	resubscribes  int
}

func newAgent(replies ...reply) *agent {
	return &agent{replies: replies, tasks: map[string]*task{}, streaming: true, cardPath: "/.well-known/agent-card.json"}
}

func (a *agent) card() Card {
	c := Card{Name: "Mochi Shop", Description: "Orders and returns", URL: "http://agent.local/a2a", Version: "1.0", ProtocolVersion: "0.3.0",
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []Skill{{ID: "order-status", Name: "Order status", Tags: []string{"orders"}}, {ID: "returns", Name: "Returns", Tags: []string{"policy"}}}}
	c.Capabilities.Streaming = a.streaming
	c.SupportsAuthenticatedExtendedCard = a.extended
	if a.secured {
		c.Security = []map[string][]string{{"bearer": {}}}
	}
	return c
}

func (a *agent) next() reply {
	r := reply{text: "no more replies"}
	if a.i < len(a.replies) {
		r = a.replies[a.i]
	}
	a.i++
	if r.state == "" {
		r.state = StateCompleted
	}
	return r
}

func (a *agent) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !a.noCard && r.URL.Path == a.cardPath {
			_ = json.NewEncoder(w).Encode(a.card())
			return
		}
		http.NotFound(w, r)
		return
	}
	var req rpcRequest
	var params struct {
		Message message `json:"message"`
		ID      string  `json:"id"`
	}
	var body struct {
		rpcRequest
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req = body.rpcRequest
	_ = json.Unmarshal(body.Params, &params)
	a.mu.Lock()
	defer a.mu.Unlock()
	write := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	if a.rpcError != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": a.rpcError})
		return
	}
	switch req.Method {
	case "agent/getAuthenticatedExtendedCard":
		a.extendedCalls++
		if r.Header.Get("Authorization") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcError{Code: -32007, Message: "authentication required"}})
			return
		}
		c := a.card()
		c.Skills = append(c.Skills, Skill{ID: "vip-returns", Name: "VIP returns", Tags: []string{"policy"}})
		write(c)
	case "tasks/resubscribe":
		a.resubscribes++
		t := a.tasks[params.ID]
		if t == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcError{Code: -32001, Message: "task not found"}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		emit := func(result any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": false,
			"status": taskStatus{State: StateWorking, Message: &message{Kind: "message", MessageID: "r1", Role: "agent", Parts: []part{{Kind: "text", Text: " after the drop"}}}}})
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": taskStatus{State: t.Status.State}})
	case "tasks/get":
		a.polls++
		t := a.tasks[params.ID]
		if t == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcError{Code: -32001, Message: "task not found"}})
			return
		}
		if t.Status.State == StateWorking {
			t.Status.State = StateCompleted
			t.Artifacts = append(t.Artifacts, artifact{ID: "a1", Parts: []part{{Kind: "text", Text: t.Status.Message.Parts[0].Text}}})
			t.Status.Message = nil
		}
		write(t)
	case "tasks/cancel":
		a.cancelled = append(a.cancelled, params.ID)
		if t := a.tasks[params.ID]; t != nil {
			t.Status.State = StateCanceled
			write(t)
			return
		}
		write(map[string]any{"id": params.ID})
	case "message/send", "message/stream":
		msg := params.Message
		a.received = append(a.received, msg)
		rep := a.next()
		// The task: the one the message refers to, or a new one.
		t := a.tasks[msg.TaskID]
		if t == nil {
			ctx := msg.ContextID
			if ctx == "" {
				ctx = fmt.Sprintf("ctx-%d", len(a.tasks)+1)
			}
			t = &task{Kind: "task", ID: fmt.Sprintf("task-%d", len(a.tasks)+1), ContextID: ctx}
			a.tasks[t.ID] = t
		}
		t.History = append(t.History, msg)
		agentMsg := &message{Kind: "message", MessageID: fmt.Sprintf("m-%d", len(t.History)+1), Role: "agent", Parts: []part{{Kind: "text", Text: rep.text}}, TaskID: t.ID, ContextID: t.ContextID}
		switch {
		case rep.working:
			t.Status = taskStatus{State: StateWorking, Message: agentMsg}
		case rep.artifact:
			t.Status = taskStatus{State: rep.state}
			t.Artifacts = append(t.Artifacts, artifact{ID: fmt.Sprintf("art-%d", len(t.Artifacts)+1), Name: "answer", Parts: []part{{Kind: "text", Text: rep.text}}})
			t.History = append(t.History, *agentMsg)
		default:
			t.Status = taskStatus{State: rep.state, Message: agentMsg}
		}
		if req.Method == "message/send" {
			write(t)
			return
		}
		// Stream: the task, then the words as status updates, then the final.
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		emit := func(result any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		emit(task{Kind: "task", ID: t.ID, ContextID: t.ContextID, Status: taskStatus{State: StateSubmitted}})
		words := strings.Fields(rep.text)
		for i, word := range words {
			if i > 0 {
				word = " " + word
			}
			if rep.drop && i == 1 {
				return // the connection is lost mid-stream
			}
			if rep.artifact {
				emit(map[string]any{"kind": "artifact-update", "taskId": t.ID, "contextId": t.ContextID, "append": i > 0, "lastChunk": i == len(words)-1,
					"artifact": artifact{ID: "art-1", Parts: []part{{Kind: "text", Text: word}}}})
			} else {
				emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": false,
					"status": taskStatus{State: StateWorking, Message: &message{Kind: "message", MessageID: fmt.Sprintf("s%d", i), Role: "agent", Parts: []part{{Kind: "text", Text: word}}}}})
			}
			time.Sleep(time.Millisecond)
		}
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": taskStatus{State: rep.state}})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rpcError{Code: -32601, Message: "method not found"}})
	}
}

func newTarget(t *testing.T, a *agent, cfg Config) (target.Target, *agent) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(srv.Close)
	cfg.URL = srv.URL + "/a2a"
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{"Authorization": "Bearer {{secrets.TOKEN}}"}
	}
	cfg.Timeout = 5 * time.Second
	cfg.PollInterval = 10 * time.Millisecond
	return New("shop", cfg), a
}

func runCtx(t *testing.T) *target.RunContext {
	return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) {
		return strings.ReplaceAll(s, "{{secrets.TOKEN}}", "s3cret"), nil
	}}
}

func TestConformance(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "send"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			targettest.Run(t, targettest.Harness{SkipDone: true, New: func(t *testing.T, replies []string) target.Target {
				var rs []reply
				for _, r := range replies {
					rs = append(rs, reply{text: r})
				}
				tgt, _ := newTarget(t, newAgent(rs...), Config{Stream: stream})
				return tgt
			}})
		})
	}
}

func TestBlockingConversation(t *testing.T) {
	a := newAgent(reply{text: "Which order?", state: StateInputRequired}, reply{text: "ORD-7 ships tomorrow.", artifact: true})
	tgt, _ := newTarget(t, a, Config{Skill: "order-status"})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Where is my order?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Which order?", turn.Text)
	assert.False(t, turn.Done)
	assert.Equal(t, 200, turn.Status)
	raw, ok := turn.Raw.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "input-required", raw["status"].(map[string]any)["state"])

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "ORD-7"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ORD-7 ships tomorrow.", turn.Text, "the artifact's text is the turn")
	assert.False(t, turn.Done, "a completed task does not end the conversation")

	require.Len(t, a.received, 2)
	assert.Empty(t, a.received[0].TaskID, "the first message opens a task")
	assert.Equal(t, "task-1", a.received[1].TaskID, "the answer to input-required continues the task")
	assert.Equal(t, "ctx-1", a.received[1].ContextID)
	assert.Equal(t, "user", a.received[1].Role)
	assert.NotEmpty(t, a.received[1].MessageID)

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1.0, arts.Metrics["a2a_tasks"])
	assert.Equal(t, 2.0, arts.Metrics["a2a_skills"])
	assert.Equal(t, 2.0, arts.Metrics["a2a_requests"])
	kinds := []string{}
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Equal(t, []string{"card", "transport", "task_input_required", "task_completed"}, kinds)
	assert.Contains(t, arts.Events[0].Detail, "order-status, returns")
	assert.Empty(t, a.cancelled, "nothing to cancel: the task completed")
}

func TestNewTaskPerCompletedExchange(t *testing.T) {
	a := newAgent(reply{text: "30 days."}, reply{text: "You are welcome."})
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	for _, say := range []string{"Return window?", "Thanks"} {
		require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: say}))
		_, err := sess.Receive(context.Background())
		require.NoError(t, err)
	}
	assert.Empty(t, a.received[1].TaskID, "after a completed task the next message opens a new one")
	assert.Equal(t, "ctx-1", a.received[1].ContextID, "in the same context")
	arts, _ := sess.Close(context.Background())
	assert.Equal(t, 2.0, arts.Metrics["a2a_tasks"])
}

func TestStreaming(t *testing.T) {
	a := newAgent(reply{text: "streamed reply here"}, reply{text: "an artifact answer", artifact: true})
	tgt, _ := newTarget(t, a, Config{Stream: true})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "streamed reply here", turn.Text)
	assert.False(t, turn.FirstTokenAt.IsZero())
	assert.True(t, turn.FirstTokenAt.Before(turn.EndedAt) || turn.FirstTokenAt.Equal(turn.EndedAt))
	events, ok := turn.Raw.([]any)
	require.True(t, ok)
	assert.Len(t, events, 5, "task, three words, final")

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "more"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "an artifact answer", turn.Text)
}

func TestStreamFallsBackWhenTheCardSaysNo(t *testing.T) {
	a := newAgent(reply{text: "plain"})
	a.streaming = false
	tgt, _ := newTarget(t, a, Config{Stream: true})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "plain", turn.Text)
	_, isMap := turn.Raw.(map[string]any)
	assert.True(t, isMap, "message/send was used")
	arts, _ := sess.Close(context.Background())
	assert.Equal(t, "no_streaming", arts.Events[1].Kind)
}

func TestWorkingTaskIsPolled(t *testing.T) {
	a := newAgent(reply{text: "took a while", working: true})
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hi"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "took a while", turn.Text)
	assert.GreaterOrEqual(t, a.polls, 1)
}

func TestFailedTaskEndsTheSession(t *testing.T) {
	a := newAgent(reply{text: "I cannot help with that", state: StateRejected})
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hack"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "I cannot help with that", turn.Text)
	assert.True(t, turn.Done)
}

func TestSkillMustBeOnTheCard(t *testing.T) {
	tgt, _ := newTarget(t, newAgent(), Config{Skill: "refunds"})
	_, err := tgt.Start(context.Background(), runCtx(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not offer skill "refunds" (has order-status, returns)`)
	tgt, _ = newTarget(t, newAgent(), Config{Skill: "Returns"})
	_, err = tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err, "a skill name matches too")
}

func TestCardMissing(t *testing.T) {
	a := newAgent(reply{text: "hi"})
	a.noCard = true
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err, "no card is a warning, not an error")
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "hello"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "hi", turn.Text)
	arts, _ := sess.Close(context.Background())
	assert.Equal(t, "card_missing", arts.Events[0].Kind)
	_, has := arts.Metrics["a2a_skills"]
	assert.False(t, has)

	tgt, _ = newTarget(t, a, Config{Skill: "returns"})
	_, err = tgt.Start(context.Background(), runCtx(t))
	require.Error(t, err, "a skill cannot be checked without a card")
}

func TestLegacyCardPath(t *testing.T) {
	a := newAgent(reply{text: "hi"})
	a.cardPath = "/.well-known/agent.json"
	tgt, _ := newTarget(t, a, Config{Skill: "returns"})
	_, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
}

func TestRPCErrorFailsTheTurn(t *testing.T) {
	a := newAgent(reply{text: "hi"})
	a.rpcError = &rpcError{Code: -32602, Message: "invalid params"}
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	err = sess.Send(context.Background(), target.UserTurn{Text: "hello"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message/send: JSON-RPC error -32602: invalid params")
}

func TestCloseCancelsAWaitingTask(t *testing.T) {
	a := newAgent(reply{text: "Which order?", state: StateInputRequired})
	tgt, _ := newTarget(t, a, Config{})
	sess, err := tgt.Start(context.Background(), runCtx(t))
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "order"}))
	_, err = sess.Receive(context.Background())
	require.NoError(t, err)
	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"task-1"}, a.cancelled)
	assert.Equal(t, "task_canceled", arts.Events[len(arts.Events)-1].Kind)
	assert.Error(t, sess.Send(context.Background(), target.UserTurn{Text: "after close"}))
}

func TestPartsText(t *testing.T) {
	got := partsText([]part{{Kind: "text", Text: "Total: "}, {Kind: "data", Data: map[string]any{"amount": 42}}, {Kind: "file", File: map[string]any{"uri": "x"}}})
	assert.Equal(t, `Total: {"amount":42}[file x]`, got)
}
