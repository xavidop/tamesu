// Package a2aserver is tamesu playing a remote agent: a small Agent2Agent
// server with a card, scripted replies and the fuzz modes, so an
// orchestrator under test can delegate to it and the suite can see what it
// delegated. With Forward set it is a recording proxy in front of a real
// remote agent instead.
//
// The server speaks JSON-RPC over HTTP itself: message/send, message/stream,
// tasks/get, tasks/cancel, tasks/resubscribe, tasks/list, the four
// tasks/pushNotificationConfig methods and agent/getAuthenticatedExtendedCard.
// Other bindings (HTTP+JSON, gRPC) are added by a Binding, which drives the
// same methods through Core.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xavidop/tamesu/target"
)

// Task states.
const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateCompleted     = "completed"
	StateInputRequired = "input-required"
	StateFailed        = "failed"
	StateCanceled      = "canceled"
)

// States lists the states a reply may leave the task in.
var States = []string{StateCompleted, StateInputRequired, StateFailed}

// Transports are the protocol bindings, as the card names them.
const (
	TransportJSONRPC  = "JSONRPC"
	TransportHTTPJSON = "HTTP+JSON"
	TransportGRPC     = "GRPC"
)

// JSON-RPC error codes, the standard ones and the A2A ones.
const (
	CodeParse           = -32700
	CodeInvalidRequest  = -32600
	CodeMethodNotFound  = -32601
	CodeInvalidParams   = -32602
	CodeInternal        = -32603
	CodeTaskNotFound    = -32001
	CodeNotCancelable   = -32002
	CodePushUnsupported = -32003
	CodeAuthRequired    = -32007
)

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

// Extended is what the authenticated extended card adds.
type Extended struct {
	Description string
	Skills      []Skill
}

// Interface is one way to reach the agent, listed on the card.
type Interface struct {
	Transport string `json:"transport"`
	URL       string `json:"url"`
}

// Config describes the remote agent.
type Config struct {
	// Addr is the address to listen on; empty means a free port on 127.0.0.1.
	Addr string
	// Name and Description go on the card; Skills too.
	Name        string
	Description string
	Skills      []Skill
	// Extended, when set, is served by agent/getAuthenticatedExtendedCard and
	// the card says so.
	Extended *Extended
	// Token, when set, is the bearer token every request must carry; the
	// card declares the scheme.
	Token string
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
	// Bindings add transports beyond JSON-RPC; each is started with the
	// server and listed on the card.
	Bindings []Binding
	// Client overrides the HTTP client of Forward and of push notifications.
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
	// Pushed is set when the reply went out as a push notification too.
	Pushed bool `json:"pushed,omitempty"`
	// Transport is the binding the message came in on.
	Transport string `json:"transport,omitempty"`
}

// Error is a JSON-RPC error, as every method reports failure.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// Binding adds a transport to a running server: it registers routes on the
// server's mux or listens on its own, and answers through Core.
type Binding interface {
	// Serve starts the binding and returns the interfaces it adds to the card.
	Serve(core Core) ([]Interface, error)
	Close() error
}

// Core is what a binding needs from the server: the card, the methods and
// the token check. Methods take and return the protocol's JSON.
type Core interface {
	URL() string
	Mux() *http.ServeMux
	Card(extended bool) map[string]any
	Handle(ctx context.Context, method string, params json.RawMessage, transport string) (json.RawMessage, *Error)
	Stream(ctx context.Context, method string, params json.RawMessage, transport string, emit func(json.RawMessage) error) *Error
	// Authorized says whether an Authorization header value carries the
	// token; always true without a token.
	Authorized(authorization string) bool
}

// Server is a running remote agent.
type Server struct {
	cfg        Config
	srv        *http.Server
	mux        *http.ServeMux
	url        string
	interfaces []Interface
	mu         sync.Mutex
	tasks      map[string]*task
	order      []string
	pushes     map[string][]pushConfig
	calls      []Delegation
	closed     bool
	wg         sync.WaitGroup
}

type part struct {
	Kind     string         `json:"kind"`
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	File     map[string]any `json:"file,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
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
	State     string   `json:"state"`
	Message   *message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
}

type task struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	ContextID string    `json:"contextId"`
	Status    status    `json:"status"`
	History   []message `json:"history,omitempty"`
}

// pushConfig is one push notification subscription of a task.
type pushConfig struct {
	ID    string         `json:"id,omitempty"`
	URL   string         `json:"url"`
	Token string         `json:"token,omitempty"`
	Auth  *pushAuth      `json:"authentication,omitempty"`
	Extra map[string]any `json:"-"`
}

type pushAuth struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials,omitempty"`
}

// taskPushConfig is the wire form: the task id and the config.
type taskPushConfig struct {
	TaskID string     `json:"taskId"`
	Config pushConfig `json:"pushNotificationConfig"`
}

// Start listens and serves the agent.
func Start(cfg Config) (*Server, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
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
	s := &Server{cfg: cfg, url: "http://" + host, tasks: map[string]*task{}, pushes: map[string][]pushConfig{}}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /.well-known/agent-card.json", s.cardHandler)
	s.mux.HandleFunc("GET /.well-known/agent.json", s.cardHandler)
	s.mux.HandleFunc("POST /", s.rpc)
	s.mux.HandleFunc("POST /a2a", s.rpc)
	s.interfaces = []Interface{{Transport: TransportJSONRPC, URL: s.url + "/a2a"}}
	for _, b := range cfg.Bindings {
		ifs, err := b.Serve(s)
		if err != nil {
			_ = l.Close()
			for _, b := range cfg.Bindings {
				_ = b.Close()
			}
			return nil, fmt.Errorf("remote agent %s: %w", cfg.Name, err)
		}
		s.interfaces = append(s.interfaces, ifs...)
	}
	s.srv = &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// URL is the agent's base URL; the JSON-RPC endpoint and the card hang off it.
func (s *Server) URL() string { return s.url }

// Mux is the server's HTTP mux, for bindings that add routes.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Interfaces lists how the agent can be reached, as the card does.
func (s *Server) Interfaces() []Interface { return append([]Interface(nil), s.interfaces...) }

// Delegations lists the messages received so far.
func (s *Server) Delegations() []Delegation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delegation(nil), s.calls...)
}

// Close stops the server and its bindings, after the push notifications in
// flight went out.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	for _, b := range s.cfg.Bindings {
		_ = b.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Authorized reports whether the Authorization header value carries the
// configured bearer token; without a token every request is authorized.
func (s *Server) Authorized(authorization string) bool {
	if s.cfg.Token == "" {
		return true
	}
	return strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer")) == s.cfg.Token && strings.HasPrefix(authorization, "Bearer")
}

// Card builds the agent card; extended adds what Extended configures.
func (s *Server) Card(extended bool) map[string]any {
	skills := make([]Skill, 0, len(s.cfg.Skills))
	add := func(list []Skill) {
		for _, sk := range list {
			if sk.Name == "" {
				sk.Name = sk.ID
			}
			if sk.Tags == nil {
				sk.Tags = []string{}
			}
			skills = append(skills, sk)
		}
	}
	add(s.cfg.Skills)
	description := s.cfg.Description
	if extended && s.cfg.Extended != nil {
		add(s.cfg.Extended.Skills)
		if s.cfg.Extended.Description != "" {
			description = s.cfg.Extended.Description
		}
	}
	if s.cfg.Forward != "" {
		description = strings.TrimSpace(description + " (recorded by tamesu)")
	}
	card := map[string]any{
		"name": s.cfg.Name, "description": description, "url": s.url + "/a2a", "version": "1.0.0", "protocolVersion": "0.3.0",
		"preferredTransport":   TransportJSONRPC,
		"additionalInterfaces": s.Interfaces(),
		"capabilities":         map[string]any{"streaming": true, "pushNotifications": true, "stateTransitionHistory": false},
		"defaultInputModes":    []string{"text/plain", "application/json"},
		"defaultOutputModes":   []string{"text/plain", "application/json"},
		"skills":               skills,
	}
	if s.cfg.Extended != nil {
		card["supportsAuthenticatedExtendedCard"] = true
	}
	if s.cfg.Token != "" {
		card["securitySchemes"] = map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}}
		card["security"] = []map[string][]string{{"bearer": {}}}
	}
	return card
}

func (s *Server) cardHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Card(false))
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// streamingMethods answer with server-sent events.
var streamingMethods = map[string]bool{"message/stream": true, "tasks/resubscribe": true}

// rpc is the JSON-RPC over HTTP binding.
func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	if !s.Authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
		s.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": Error{Code: CodeAuthRequired, Message: "a bearer token is required"}})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.rpcError(w, nil, CodeParse, "parse error")
		return
	}
	if s.cfg.Forward != "" && s.cfg.Fuzz == nil {
		var params struct {
			Message message `json:"message"`
		}
		_ = json.Unmarshal(req.Params, &params)
		s.forward(w, r, req, params.Message, body)
		return
	}
	if streamingMethods[req.Method] {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fl, _ := w.(http.Flusher)
		emit := func(v json.RawMessage) error {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return err
			}
			if fl != nil {
				fl.Flush()
			}
			return nil
		}
		if e := s.Stream(r.Context(), req.Method, req.Params, TransportJSONRPC, emit); e != nil {
			// Before the first event the error is a plain JSON-RPC response.
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": e})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}
		return
	}
	result, e := s.Handle(r.Context(), req.Method, req.Params, TransportJSONRPC)
	if e != nil {
		s.rpcError(w, req.ID, e.Code, e.Message)
		return
	}
	s.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) rpcError(w http.ResponseWriter, id any, code int, msg string) {
	s.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": Error{Code: code, Message: msg}})
}

// sendParams are the parameters of message/send and message/stream.
type sendParams struct {
	Message       message `json:"message"`
	Configuration struct {
		Blocking      *bool       `json:"blocking"`
		HistoryLength *int        `json:"historyLength"`
		Push          *pushConfig `json:"pushNotificationConfig"`
	} `json:"configuration"`
}

// idParams name a task, and for the push config methods a config.
type idParams struct {
	ID            string `json:"id"`
	ConfigID      string `json:"pushNotificationConfigId"`
	HistoryLength *int   `json:"historyLength"`
}

func marshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Handle runs one unary method and returns its result as JSON.
func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage, transport string) (json.RawMessage, *Error) {
	switch method {
	case "message/send":
		var p sendParams
		if err := json.Unmarshal(params, &p); err != nil || len(p.Message.Parts) == 0 && p.Message.MessageID == "" {
			return nil, &Error{Code: CodeInvalidParams, Message: "message/send needs a message"}
		}
		view, d := s.answer(ctx, p.Message, p.Configuration.Push)
		d.Transport = transport
		s.record(d)
		return marshal(trimHistory(view, p.Configuration.HistoryLength)), nil
	case "tasks/get":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		t := s.tasks[p.ID]
		s.mu.Unlock()
		if t == nil {
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		return marshal(s.snapshot(t, p.HistoryLength)), nil
	case "tasks/list":
		s.mu.Lock()
		list := make([]task, 0, len(s.order))
		for _, id := range s.order {
			list = append(list, s.view(s.tasks[id], nil))
		}
		s.mu.Unlock()
		return marshal(map[string]any{"tasks": list}), nil
	case "tasks/cancel":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		t := s.tasks[p.ID]
		if t != nil {
			if t.Status.State == StateCompleted || t.Status.State == StateFailed || t.Status.State == StateCanceled {
				s.mu.Unlock()
				return nil, &Error{Code: CodeNotCancelable, Message: "the task is already " + t.Status.State}
			}
			t.Status = status{State: StateCanceled, Timestamp: time.Now().UTC().Format(time.RFC3339)}
		}
		s.mu.Unlock()
		if t == nil {
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		s.notify(t)
		return marshal(s.snapshot(t, nil)), nil
	case "tasks/pushNotificationConfig/set":
		var p taskPushConfig
		if err := json.Unmarshal(params, &p); err != nil || p.TaskID == "" || p.Config.URL == "" {
			return nil, &Error{Code: CodeInvalidParams, Message: "needs taskId and pushNotificationConfig.url"}
		}
		s.mu.Lock()
		t := s.tasks[p.TaskID]
		if t == nil {
			s.mu.Unlock()
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		if p.Config.ID == "" {
			p.Config.ID = uuid.NewString()
		}
		replaced := false
		for i, c := range s.pushes[p.TaskID] {
			if c.ID == p.Config.ID {
				s.pushes[p.TaskID][i] = p.Config
				replaced = true
			}
		}
		if !replaced {
			s.pushes[p.TaskID] = append(s.pushes[p.TaskID], p.Config)
		}
		s.mu.Unlock()
		return marshal(p), nil
	case "tasks/pushNotificationConfig/get":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.tasks[p.ID] == nil {
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		for _, c := range s.pushes[p.ID] {
			if p.ConfigID == "" || c.ID == p.ConfigID {
				return marshal(taskPushConfig{TaskID: p.ID, Config: c}), nil
			}
		}
		return nil, &Error{Code: CodeTaskNotFound, Message: "push notification config not found"}
	case "tasks/pushNotificationConfig/list":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.tasks[p.ID] == nil {
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		list := make([]taskPushConfig, 0, len(s.pushes[p.ID]))
		for _, c := range s.pushes[p.ID] {
			list = append(list, taskPushConfig{TaskID: p.ID, Config: c})
		}
		return marshal(list), nil
	case "tasks/pushNotificationConfig/delete":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.tasks[p.ID] == nil {
			return nil, &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		kept := s.pushes[p.ID][:0]
		for _, c := range s.pushes[p.ID] {
			if c.ID != p.ConfigID {
				kept = append(kept, c)
			}
		}
		s.pushes[p.ID] = kept
		return json.RawMessage("null"), nil
	case "agent/getAuthenticatedExtendedCard":
		return marshal(s.Card(true)), nil
	case "message/stream", "tasks/resubscribe":
		return nil, &Error{Code: CodeInvalidRequest, Message: method + " streams; use Stream"}
	}
	return nil, &Error{Code: CodeMethodNotFound, Message: "method not found"}
}

// Stream runs one streaming method, emitting each event as JSON.
func (s *Server) Stream(ctx context.Context, method string, params json.RawMessage, transport string, emit func(json.RawMessage) error) *Error {
	switch method {
	case "message/stream":
		var p sendParams
		if err := json.Unmarshal(params, &p); err != nil || len(p.Message.Parts) == 0 && p.Message.MessageID == "" {
			return &Error{Code: CodeInvalidParams, Message: "message/stream needs a message"}
		}
		view, d := s.answer(ctx, p.Message, nil)
		d.Streamed = true
		d.Transport = transport
		s.record(d)
		id, cid, st := view.ID, view.ContextID, view.Status
		if err := emit(marshal(task{Kind: "task", ID: id, ContextID: cid, Status: status{State: StateSubmitted}})); err != nil {
			return nil
		}
		_ = emit(marshal(map[string]any{"kind": "status-update", "taskId": id, "contextId": cid, "final": true, "status": st}))
		return nil
	case "tasks/resubscribe":
		var p idParams
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		t := s.tasks[p.ID]
		var view task
		if t != nil {
			view = s.view(t, nil)
		}
		s.mu.Unlock()
		if t == nil {
			return &Error{Code: CodeTaskNotFound, Message: "task not found"}
		}
		if err := emit(marshal(view)); err != nil {
			return nil
		}
		_ = emit(marshal(map[string]any{"kind": "status-update", "taskId": view.ID, "contextId": view.ContextID, "final": true, "status": view.Status}))
		return nil
	case "message/send", "tasks/get", "tasks/cancel", "tasks/list", "agent/getAuthenticatedExtendedCard":
		return &Error{Code: CodeInvalidRequest, Message: method + " does not stream; use Handle"}
	}
	return &Error{Code: CodeMethodNotFound, Message: "method not found"}
}

// snapshot is view under the lock.
func (s *Server) snapshot(t *task, historyLength *int) task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view(t, historyLength)
}

// view copies a task for the wire, trimming the history when asked. The
// caller holds s.mu.
func (s *Server) view(t *task, historyLength *int) task {
	out := *t
	out.History = append([]message(nil), t.History...)
	return trimHistory(out, historyLength)
}

// trimHistory keeps the last historyLength messages of a task copy.
func trimHistory(out task, historyLength *int) task {
	if historyLength != nil && *historyLength >= 0 && len(out.History) > *historyLength {
		out.History = out.History[len(out.History)-*historyLength:]
	}
	return out
}

// textOf joins the text parts of a message; data parts read as JSON and file
// parts as their name, so a scripted reply can match on them.
func textOf(m message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		switch {
		case p.Kind == "text":
			b.WriteString(p.Text)
		case p.Kind == "data" && len(p.Data) > 0:
			if j, err := json.Marshal(p.Data); err == nil {
				b.Write(j)
			}
		case p.Kind == "file" && p.File != nil:
			name, _ := p.File["name"].(string)
			if name == "" {
				name, _ = p.File["uri"].(string)
			}
			b.WriteString("[file " + name + "]")
		}
	}
	return b.String()
}

// answer builds the task for a message: the task it continues or a new
// one, with the reply the script (or the fuzz) gives, and returns the copy
// the caller sees. With a push config that copy is the task as submitted:
// it is finished in the background and the finished task is pushed, which
// is what a caller that asked for push notifications expects.
func (s *Server) answer(ctx context.Context, msg message, push *pushConfig) (task, Delegation) {
	text := textOf(msg)
	s.mu.Lock()
	t := s.tasks[msg.TaskID]
	if t == nil {
		cid := msg.ContextID
		if cid == "" {
			cid = uuid.NewString()
		}
		t = &task{Kind: "task", ID: uuid.NewString(), ContextID: cid, Status: status{State: StateSubmitted}}
		s.tasks[t.ID] = t
		s.order = append(s.order, t.ID)
	}
	if push != nil && push.URL != "" {
		if push.ID == "" {
			push.ID = uuid.NewString()
		}
		s.pushes[t.ID] = append(s.pushes[t.ID], *push)
	}
	msg.TaskID, msg.ContextID = t.ID, t.ContextID
	t.History = append(t.History, msg)
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
	d.Reply, d.State = reply.Text, reply.State
	if push != nil && push.URL != "" {
		d.Pushed = true
		s.mu.Lock()
		t.Status = status{State: StateSubmitted, Timestamp: time.Now().UTC().Format(time.RFC3339)}
		view := s.view(t, nil)
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.finish(t, reply)
			s.notify(t)
		}()
		return view, d
	}
	s.finish(t, reply)
	s.notify(t)
	return s.snapshot(t, nil), d
}

// finish writes the reply into the task.
func (s *Server) finish(t *task, reply Reply) {
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
	t.History = append(t.History, *agentMsg)
	t.Status = status{State: reply.State, Message: agentMsg, Timestamp: time.Now().UTC().Format(time.RFC3339)}
	s.mu.Unlock()
}

// notify posts the task to every push notification URL subscribed to it,
// with the token header the subscriber gave.
func (s *Server) notify(t *task) {
	s.mu.Lock()
	configs := append([]pushConfig(nil), s.pushes[t.ID]...)
	view := s.view(t, nil)
	s.mu.Unlock()
	if len(configs) == 0 {
		return
	}
	body := marshal(view)
	for _, c := range configs {
		s.wg.Add(1)
		go func(c pushConfig) {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if c.Token != "" {
				req.Header.Set("X-A2A-Notification-Token", c.Token)
			}
			if c.Auth != nil && c.Auth.Credentials != "" {
				scheme := "Bearer"
				if len(c.Auth.Schemes) > 0 && !strings.EqualFold(c.Auth.Schemes[0], "bearer") {
					scheme = c.Auth.Schemes[0]
				}
				req.Header.Set("Authorization", scheme+" "+c.Auth.Credentials)
			}
			resp, err := s.cfg.Client.Do(req)
			if err != nil {
				s.cfg.Logger.Warn("push notification failed", "agent", s.cfg.Name, "url", c.URL, "err", err)
				return
			}
			_ = resp.Body.Close()
		}(c)
	}
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
		s.rpcError(w, req.ID, CodeInternal, err.Error())
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
		s.rpcError(w, req.ID, CodeInternal, "forward: "+err.Error())
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
	d := Delegation{At: time.Now(), Message: textOf(msg), Forwarded: true, Streamed: req.Method == "message/stream", Transport: TransportJSONRPC}
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
		if record {
			s.record(d)
		}
		return
	}
	// Record before the body goes out, so a caller that has read the reply
	// also sees the delegation.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	d.Reply, d.State, d.TaskID, d.ContextID = replyOf(raw)
	if record {
		s.record(d)
	}
	_, _ = w.Write(raw)
}

func (s *Server) record(d Delegation) {
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

// Methods lists every JSON-RPC method the server answers, sorted.
func Methods() []string {
	m := []string{"message/send", "message/stream", "tasks/get", "tasks/list", "tasks/cancel", "tasks/resubscribe",
		"tasks/pushNotificationConfig/set", "tasks/pushNotificationConfig/get", "tasks/pushNotificationConfig/list", "tasks/pushNotificationConfig/delete",
		"agent/getAuthenticatedExtendedCard"}
	sort.Strings(m)
	return m
}
