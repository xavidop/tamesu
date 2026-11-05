// Package a2aserver is tamesu playing a remote agent: a small Agent2Agent
// server with a card, scripted replies and the fuzz modes, so an
// orchestrator under test can delegate to it and the suite can see what it
// delegated. With Forward set it is a recording proxy in front of a real
// remote agent instead.
package a2aserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
)

// Task states.
const (
	StateCompleted     = "completed"
	StateInputRequired = "input-required"
	StateFailed        = "failed"
	StateCanceled      = "canceled"
)

// States lists the states a reply may leave the task in.
var States = []string{StateCompleted, StateInputRequired, StateFailed}

// Skill is one skill of the card.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags"`
}

// Reply is what the agent answers: the first Reply whose When matches the
// message wins, else the default.
type Reply struct {
	// When selects the messages this reply answers; nil matches any.
	When *regexp.Regexp
	// Text is the agent's words; Data an optional data part.
	Text string
	Data map[string]any
	// State is the task's state after the reply; empty means completed.
	State string
}

// Config describes the remote agent.
type Config struct {
	// Addr is the address to listen on; empty means a free port on 127.0.0.1.
	Addr string
	// Name and Description go on the card; Skills too.
	Name        string
	Description string
	Skills      []Skill
	// Default answers the messages no Replies entry matches.
	Default Reply
	Replies []Reply
	// Forward makes the server a recording proxy: every JSON-RPC request is
	// forwarded to this URL (a real agent's endpoint) with ForwardHeaders,
	// and both directions are recorded.
	Forward        string
	ForwardHeaders map[string]string
	// Fuzz replaces the reply, as a fuzzed tool result would (and, with
	// Forward, answers instead of forwarding).
	Fuzz *target.ToolFuzz
	// Client overrides the HTTP client of Forward.
	Client *http.Client
	Logger *slog.Logger
}

// Delegation is one message the orchestrator sent to the agent.
type Delegation struct {
	At        time.Time `json:"at"`
	Message   string    `json:"message"`
	Reply     string    `json:"reply"`
	State     string    `json:"state"`
	TaskID    string    `json:"task_id"`
	ContextID string    `json:"context_id"`
	Forwarded bool      `json:"forwarded,omitempty"`
	Fuzzed    string    `json:"fuzzed,omitempty"`
	Streamed  bool      `json:"streamed,omitempty"`
}

// Server is a running remote agent.
type Server struct {
	cfg    Config
	srv    *http.Server
	url    string
	mu     sync.Mutex
	tasks  map[string]*task
	calls  []Delegation
	closed bool
}

type part struct {
	Kind string         `json:"kind"`
	Text string         `json:"text,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

type message struct {
	Kind      string         `json:"kind"`
	MessageID string         `json:"messageId"`
	Role      string         `json:"role"`
	Parts     []part         `json:"parts"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type status struct {
	State   string   `json:"state"`
	Message *message `json:"message,omitempty"`
}

type task struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	ContextID string    `json:"contextId"`
	Status    status    `json:"status"`
	History   []message `json:"history,omitempty"`
}

// Start listens and serves the agent.
func Start(cfg Config) (*Server, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Name == "" {
		cfg.Name = "remote agent"
	}
	if cfg.Default.State == "" {
		cfg.Default.State = StateCompleted
	}
	for i := range cfg.Replies {
		if cfg.Replies[i].State == "" {
			cfg.Replies[i].State = StateCompleted
		}
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("remote agent %s: listen %s: %w", cfg.Name, addr, err)
	}
	host := l.Addr().String()
	if h, port, err := net.SplitHostPort(host); err == nil && (h == "" || h == "::" || h == "0.0.0.0") {
		host = net.JoinHostPort("127.0.0.1", port)
	}
	s := &Server{cfg: cfg, url: "http://" + host, tasks: map[string]*task{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", s.card)
	mux.HandleFunc("GET /.well-known/agent.json", s.card)
	mux.HandleFunc("POST /", s.rpc)
	mux.HandleFunc("POST /a2a", s.rpc)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// URL is the agent's base URL; the JSON-RPC endpoint and the card hang off it.
func (s *Server) URL() string { return s.url }

// Delegations lists the messages received so far.
func (s *Server) Delegations() []Delegation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delegation(nil), s.calls...)
}

// Close stops the server.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) card(w http.ResponseWriter, _ *http.Request) {
	skills := make([]Skill, 0, len(s.cfg.Skills))
	for _, sk := range s.cfg.Skills {
		if sk.Name == "" {
			sk.Name = sk.ID
		}
		if sk.Tags == nil {
			sk.Tags = []string{}
		}
		skills = append(skills, sk)
	}
	card := map[string]any{
		"name": s.cfg.Name, "description": s.cfg.Description, "url": s.url + "/a2a", "version": "1.0.0", "protocolVersion": "0.3.0",
		"capabilities":       map[string]any{"streaming": true},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             skills,
	}
	if s.cfg.Forward != "" {
		card["description"] = strings.TrimSpace(s.cfg.Description + " (recorded by tamesu)")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(card)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.rpcError(w, nil, -32700, "parse error")
		return
	}
	var params struct {
		Message message `json:"message"`
		ID      string  `json:"id"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if s.cfg.Forward != "" && s.cfg.Fuzz == nil {
		s.forward(w, r, req, params.Message, body)
		return
	}
	switch req.Method {
	case "tasks/get":
		s.mu.Lock()
		t := s.tasks[params.ID]
		s.mu.Unlock()
		if t == nil {
			s.rpcError(w, req.ID, -32001, "task not found")
			return
		}
		s.result(w, req.ID, t)
	case "tasks/cancel":
		s.mu.Lock()
		t := s.tasks[params.ID]
		if t != nil {
			t.Status = status{State: StateCanceled}
		}
		s.mu.Unlock()
		if t == nil {
			s.rpcError(w, req.ID, -32001, "task not found")
			return
		}
		s.result(w, req.ID, t)
	case "message/send", "message/stream":
		t, d := s.answer(r.Context(), params.Message)
		d.Streamed = req.Method == "message/stream"
		s.mu.Lock()
		s.calls = append(s.calls, d)
		s.mu.Unlock()
		if req.Method == "message/send" {
			s.result(w, req.ID, t)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, _ := w.(http.Flusher)
		emit := func(v any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		emit(task{Kind: "task", ID: t.ID, ContextID: t.ContextID, Status: status{State: "submitted"}})
		emit(map[string]any{"kind": "status-update", "taskId": t.ID, "contextId": t.ContextID, "final": true, "status": t.Status})
	default:
		s.rpcError(w, req.ID, -32601, "method not found")
	}
}

func (s *Server) result(w http.ResponseWriter, id, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": v})
}

func (s *Server) rpcError(w http.ResponseWriter, id any, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

// textOf joins the text parts of a message.
func textOf(m message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		} else if p.Kind == "data" && len(p.Data) > 0 {
			if j, err := json.Marshal(p.Data); err == nil {
				b.Write(j)
			}
		}
	}
	return b.String()
}

// answer builds the task for a message: the task it continues or a new
// one, with the reply the script (or the fuzz) gives.
func (s *Server) answer(ctx context.Context, msg message) (*task, Delegation) {
	text := textOf(msg)
	s.mu.Lock()
	t := s.tasks[msg.TaskID]
	if t == nil {
		cid := msg.ContextID
		if cid == "" {
			cid = uuid.NewString()
		}
		t = &task{Kind: "task", ID: uuid.NewString(), ContextID: cid}
		s.tasks[t.ID] = t
	}
	s.mu.Unlock()
	reply := s.cfg.Default
	for _, r := range s.cfg.Replies {
		if r.When == nil || r.When.MatchString(text) {
			reply = r
			break
		}
	}
	d := Delegation{At: time.Now(), Message: text, TaskID: t.ID, ContextID: t.ContextID}
	if f := s.cfg.Fuzz; f != nil {
		if err := f.Wait(ctx); err != nil {
			reply = Reply{Text: "", State: StateFailed}
		} else {
			reply = fuzzed(*f, reply)
		}
		d.Fuzzed = f.Mode
	}
	agentMsg := &message{Kind: "message", MessageID: uuid.NewString(), Role: "agent", TaskID: t.ID, ContextID: t.ContextID}
	if reply.Text != "" {
		agentMsg.Parts = append(agentMsg.Parts, part{Kind: "text", Text: reply.Text})
	}
	if len(reply.Data) > 0 {
		agentMsg.Parts = append(agentMsg.Parts, part{Kind: "data", Data: reply.Data})
	}
	if agentMsg.Parts == nil {
		agentMsg.Parts = []part{}
	}
	s.mu.Lock()
	t.History = append(t.History, msg, *agentMsg)
	t.Status = status{State: reply.State, Message: agentMsg}
	s.mu.Unlock()
	d.Reply, d.State = reply.Text, reply.State
	return t, d
}

// fuzzed is the reply a fuzz mode gives instead of the scripted one.
func fuzzed(f target.ToolFuzz, normal Reply) Reply {
	switch f.Mode {
	case target.FuzzError:
		return Reply{Text: "internal error: the service is unavailable", State: StateFailed}
	case target.FuzzEmpty, target.FuzzNull:
		return Reply{Text: "", State: StateCompleted}
	case target.FuzzDelay, "":
		return normal
	}
	out := f.Output(nil)
	switch v := out.(type) {
	case nil:
		return Reply{Text: "", State: StateCompleted}
	case string:
		return Reply{Text: v, State: StateCompleted}
	case map[string]any:
		if f.Mode == target.FuzzHostile {
			return Reply{Text: target.HostileResult, State: StateCompleted}
		}
		return Reply{Text: "", Data: v, State: StateCompleted}
	default:
		return Reply{Text: fmt.Sprint(v), State: StateCompleted}
	}
}

// forward proxies one JSON-RPC request to the real agent and records the
// message and the reply it carried.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, req rpcRequest, msg message, body []byte) {
	fctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	preq, err := http.NewRequestWithContext(fctx, http.MethodPost, s.cfg.Forward, bytes.NewReader(body))
	if err != nil {
		s.rpcError(w, req.ID, -32000, err.Error())
		return
	}
	for k, v := range r.Header {
		if k == "Host" || k == "Content-Length" {
			continue
		}
		preq.Header[k] = v
	}
	for k, v := range s.cfg.ForwardHeaders {
		preq.Header.Set(k, v)
	}
	resp, err := s.cfg.Client.Do(preq)
	if err != nil {
		s.rpcError(w, req.ID, -32000, "forward: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	streaming := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	for k, v := range resp.Header {
		// An event stream is relayed line by line and ends when this handler
		// returns; a copied Content-Length would let the client finish before
		// the delegation is recorded.
		if streaming && k == "Content-Length" {
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	record := req.Method == "message/send" || req.Method == "message/stream"
	d := Delegation{At: time.Now(), Message: textOf(msg), Forwarded: true, Streamed: req.Method == "message/stream"}
	if streaming {
		fl, _ := w.(http.Flusher)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
		var text strings.Builder
		for sc.Scan() {
			line := sc.Text()
			_, _ = fmt.Fprintln(w, line)
			if line == "" && fl != nil {
				fl.Flush()
			}
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				t, state, id, cid := replyOf([]byte(strings.TrimSpace(data)))
				text.WriteString(t)
				if state != "" {
					d.State = state
				}
				if id != "" {
					d.TaskID, d.ContextID = id, cid
				}
			}
		}
		d.Reply = text.String()
		s.record(record, d)
		return
	}
	// Record before the body goes out, so a caller that has read the reply
	// also sees the delegation.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	d.Reply, d.State, d.TaskID, d.ContextID = replyOf(raw)
	s.record(record, d)
	_, _ = w.Write(raw)
}

func (s *Server) record(record bool, d Delegation) {
	if !record {
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, d)
	s.mu.Unlock()
}

// replyOf reads the agent's words and the task state out of a JSON-RPC
// response or event.
func replyOf(raw []byte) (text, state, taskID, contextID string) {
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &env) != nil || len(env.Result) == 0 {
		return "", "", "", ""
	}
	var ev struct {
		Kind      string                   `json:"kind"`
		ID        string                   `json:"id"`
		TaskID    string                   `json:"taskId"`
		ContextID string                   `json:"contextId"`
		Status    status                   `json:"status"`
		Parts     []part                   `json:"parts"`
		Artifact  *struct{ Parts []part }  `json:"artifact"`
		Artifacts []struct{ Parts []part } `json:"artifacts"`
	}
	if json.Unmarshal(env.Result, &ev) != nil {
		return "", "", "", ""
	}
	var b strings.Builder
	switch ev.Kind {
	case "message":
		b.WriteString(textOf(message{Parts: ev.Parts}))
	case "task":
		if ev.Status.Message != nil {
			b.WriteString(textOf(*ev.Status.Message))
		}
		for _, a := range ev.Artifacts {
			b.WriteString(textOf(message{Parts: a.Parts}))
		}
		taskID, contextID = ev.ID, ev.ContextID
	case "status-update":
		if ev.Status.Message != nil {
			b.WriteString(textOf(*ev.Status.Message))
		}
		taskID, contextID = ev.TaskID, ev.ContextID
	case "artifact-update":
		if ev.Artifact != nil {
			b.WriteString(textOf(message{Parts: ev.Artifact.Parts}))
		}
	}
	return b.String(), ev.Status.State, taskID, contextID
}

// ErrNoReply is returned by Validate when the agent has nothing to say.
var ErrNoReply = errors.New("a remote agent needs a reply, replies or forward")

// Validate checks a config before Start.
func (c Config) Validate() error {
	if c.Default.Text == "" && len(c.Default.Data) == 0 && len(c.Replies) == 0 && c.Forward == "" {
		return ErrNoReply
	}
	return nil
}
