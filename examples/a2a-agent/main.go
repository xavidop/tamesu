// A tiny A2A agent for the shoe shop: order status and the returns policy
// over the Agent2Agent protocol (JSON-RPC, an agent card, message/send and
// message/stream with server-sent events, tasks that ask for the order id,
// push notifications, tasks/resubscribe and an authenticated extended card).
// examples/a2a-agent/suite.yaml tests it with the a2a target.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var orders = map[string]string{
	"ORD-1001": "ORD-1001 (Mochi Runner 42) shipped with DHL and arrives in 2 days.",
	"ORD-1002": "ORD-1002 (Mochi Loafer 39) was delivered 40 days ago.",
}

const returnsPolicy = "Returns are accepted within 30 days of delivery. After 30 days there are no exceptions; a 10% voucher on the next order is offered instead."

var orderRE = regexp.MustCompile(`(?i)\bORD-\d+\b`)

type part struct {
	Kind string         `json:"kind"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

type message struct {
	Kind      string `json:"kind"`
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	Parts     []part `json:"parts"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

type status struct {
	State   string   `json:"state"`
	Message *message `json:"message,omitempty"`
}

type artifact struct {
	ID    string `json:"artifactId"`
	Name  string `json:"name,omitempty"`
	Parts []part `json:"parts"`
}

type task struct {
	Kind      string     `json:"kind"`
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    status     `json:"status"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	History   []message  `json:"history,omitempty"`
}

// pushConfig is where a caller asked for a task's updates to be posted.
type pushConfig struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

type agent struct {
	mu     sync.Mutex
	tasks  map[string]*task
	pushes map[string]pushConfig
	n      int
}

// card is the public card; extended adds the skill only authenticated
// callers see.
func (a *agent) card(base string, extended bool) map[string]any {
	skills := []map[string]any{
		{"id": "order-status", "name": "Order status", "description": "Where an order is and when it arrives", "tags": []string{"orders"}, "examples": []string{"Where is order ORD-1001?"}},
		{"id": "returns", "name": "Returns", "description": "The returns policy", "tags": []string{"policy"}, "examples": []string{"Can I return shoes bought 40 days ago?"}},
	}
	if extended {
		skills = append(skills, map[string]any{"id": "vip-returns", "name": "VIP returns", "description": "Returns without a time limit for VIP customers", "tags": []string{"policy", "vip"}})
	}
	return map[string]any{
		"name": "Mochi Shoes", "description": "Order status and the returns policy of the Mochi Shoes shop.",
		"url": base + "/a2a", "version": "1.0.0", "protocolVersion": "0.3.0", "preferredTransport": "JSONRPC",
		"capabilities":                      map[string]any{"streaming": true, "pushNotifications": true},
		"defaultInputModes":                 []string{"text/plain", "application/json"},
		"defaultOutputModes":                []string{"text/plain"},
		"supportsAuthenticatedExtendedCard": true,
		"skills":                            skills,
	}
}

// notify posts a task to the URL subscribed to it, with the caller's token.
func (a *agent) notify(t *task) {
	a.mu.Lock()
	cfg, ok := a.pushes[t.ID]
	body, _ := json.Marshal(t)
	a.mu.Unlock()
	if !ok {
		return
	}
	req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("X-A2A-Notification-Token", cfg.Token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// answer decides what the agent says: it asks for the order id when the
// customer talks about an order without one.
func answer(text string) (reply string, needsInput bool) {
	lower := strings.ToLower(text)
	if id := orderRE.FindString(text); id != "" {
		if o, ok := orders[strings.ToUpper(id)]; ok {
			return o, false
		}
		return fmt.Sprintf("I cannot find order %s. Please check the number on your confirmation e-mail.", strings.ToUpper(id)), false
	}
	switch {
	case strings.Contains(lower, "order") || strings.Contains(lower, "arrive") || strings.Contains(lower, "deliver"):
		return "Which order is it? The number looks like ORD-1001.", true
	case strings.Contains(lower, "return") || strings.Contains(lower, "refund") || strings.Contains(lower, "days"):
		return returnsPolicy, false
	case strings.Contains(lower, "bye") || strings.Contains(lower, "thank"):
		return "You are welcome. Have a nice day!", false
	}
	return "I can tell you where an order is or explain the returns policy. What do you need?", false
}

func (a *agent) rpcError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (a *agent) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.rpcError(w, nil, -32700, "parse error")
		return
	}
	var params struct {
		Message       message `json:"message"`
		ID            string  `json:"id"`
		Configuration struct {
			Push *pushConfig `json:"pushNotificationConfig"`
		} `json:"configuration"`
	}
	_ = json.Unmarshal(req.Params, &params)
	a.mu.Lock()
	defer a.mu.Unlock()
	result := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
	}
	sse := func() func(v any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, _ := w.(http.Flusher)
		return func(v any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
	}
	switch req.Method {
	case "agent/getAuthenticatedExtendedCard":
		if r.Header.Get("Authorization") == "" {
			a.rpcError(w, req.ID, -32007, "authentication required")
			return
		}
		result(a.card("http://"+r.Host, true))
	case "tasks/resubscribe":
		t := a.tasks[params.ID]
		if t == nil {
			a.rpcError(w, req.ID, -32001, "task not found")
			return
		}
		emit := sse()
		emit(t)
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": t.Status})
	case "tasks/get":
		if t := a.tasks[params.ID]; t != nil {
			result(t)
			return
		}
		a.rpcError(w, req.ID, -32001, "task not found")
	case "tasks/cancel":
		if t := a.tasks[params.ID]; t != nil {
			t.Status = status{State: "canceled"}
			result(t)
			return
		}
		a.rpcError(w, req.ID, -32001, "task not found")
	case "message/send", "message/stream":
		msg := params.Message
		t := a.tasks[msg.TaskID]
		if t == nil {
			a.n++
			ctx := msg.ContextID
			if ctx == "" {
				ctx = fmt.Sprintf("ctx-%d", a.n)
			}
			t = &task{Kind: "task", ID: fmt.Sprintf("task-%d", a.n), ContextID: ctx}
			a.tasks[t.ID] = t
		}
		t.History = append(t.History, msg)
		text := ""
		for _, p := range msg.Parts {
			if p.Kind == "text" {
				text += p.Text
			}
		}
		for _, p := range msg.Parts {
			if p.Kind == "data" {
				if id, ok := p.Data["order"].(string); ok {
					text += " " + id
				}
			}
		}
		reply, needsInput := answer(text)
		agentMsg := &message{Kind: "message", MessageID: fmt.Sprintf("%s-m%d", t.ID, len(t.History)+1), Role: "agent", Parts: []part{{Kind: "text", Text: reply}}, TaskID: t.ID, ContextID: t.ContextID}
		finish := func() {
			switch {
			case needsInput:
				t.Status = status{State: "input-required", Message: agentMsg}
			default:
				t.Status = status{State: "completed"}
				t.Artifacts = append(t.Artifacts, artifact{ID: fmt.Sprintf("%s-a%d", t.ID, len(t.Artifacts)+1), Name: "answer", Parts: []part{{Kind: "text", Text: reply}}})
				t.History = append(t.History, *agentMsg)
			}
		}
		if p := params.Configuration.Push; p != nil && p.URL != "" && req.Method == "message/send" {
			// The caller wants the answer pushed: the task is submitted now
			// and finished a moment later, like real work would be.
			a.pushes[t.ID] = *p
			t.Status = status{State: "submitted"}
			result(t)
			go func() {
				time.Sleep(20 * time.Millisecond)
				a.mu.Lock()
				finish()
				a.mu.Unlock()
				a.notify(t)
			}()
			return
		}
		finish()
		if req.Method == "message/send" {
			result(t)
			return
		}
		emit := sse()
		emit(task{Kind: "task", ID: t.ID, ContextID: t.ContextID, Status: status{State: "submitted"}})
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": false, "status": status{State: "working"}})
		if needsInput {
			emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": t.Status})
			return
		}
		words := strings.Fields(reply)
		for i, word := range words {
			if i > 0 {
				word = " " + word
			}
			emit(map[string]any{"kind": "artifact-update", "taskId": t.ID, "contextId": t.ContextID, "append": i > 0, "lastChunk": i == len(words)-1,
				"artifact": artifact{ID: t.Artifacts[len(t.Artifacts)-1].ID, Name: "answer", Parts: []part{{Kind: "text", Text: word}}}})
			time.Sleep(5 * time.Millisecond)
		}
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": status{State: "completed"}})
	default:
		a.rpcError(w, req.ID, -32601, "method not found")
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "address to listen on")
	flag.Parse()
	a := &agent{tasks: map[string]*task{}, pushes: map[string]pushConfig{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.card("http://"+r.Host, false))
	})
	mux.HandleFunc("POST /a2a", a.serve)
	log.Printf("a2a-agent listening on http://%s/a2a (card at /.well-known/agent-card.json)", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
