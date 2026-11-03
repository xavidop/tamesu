// Package model is the model target: a system prompt and tool definitions
// tested directly against a model, with no agent server in between. The
// simulated user's turns go to the model with the prompt; the model's tool
// calls are answered from the results configured in the suite, and its
// text is the agent's turn. It is where a prompt is regression-tested
// before (or without) an agent runtime.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xavidop/tamesu/target"
)

// DefaultMaxToolRounds bounds the tool-call rounds of one turn.
const DefaultMaxToolRounds = 5

// DefaultDoneTool is the tool whose call ends the session.
const DefaultDoneTool = "end_session"

// ErrNoModel is returned when the build has no model provider.
var ErrNoModel = errors.New("the model target needs a model provider (the llm module)")

// Tool is a tool offered to the model with the result tamesu answers.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON schema of the arguments (an object schema).
	Parameters map[string]any
	// Result is what the call returns (any JSON; string leaves may use
	// {{args.<key>}}). Nil answers with an error object, so a prompt that
	// depends on the tool shows it.
	Result any
}

// Config is the target's configuration.
type Config struct {
	// Model is a Genkit model reference; "" uses the project's default.
	Model string
	// Fallback lists the models tried, in order, when Model fails; a turn
	// answered by one records a model_fallback event.
	Fallback []string
	// System is the prompt under test; {{vars.x}} and {{secrets.X}} render.
	System string
	Tools  []Tool
	// DoneTool is the tool whose call ends the session (default end_session).
	DoneTool    string
	Temperature *float64
	MaxTokens   int
	// MaxToolRounds bounds the call-answer rounds in one turn (default 5).
	MaxToolRounds int
}

// Target is the model target.
type Target struct {
	name string
	cfg  Config
}

// New returns the target.
func New(name string, cfg Config) *Target {
	if cfg.DoneTool == "" {
		cfg.DoneTool = DefaultDoneTool
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = DefaultMaxToolRounds
	}
	return &Target{name: name, cfg: cfg}
}

// Name implements target.Target.
func (t *Target) Name() string { return t.name }

// Start implements target.Target: it renders the system prompt and opens a
// conversation.
func (t *Target) Start(_ context.Context, run *target.RunContext) (target.Session, error) {
	if run.Chat == nil {
		return nil, fmt.Errorf("model %s: %w", t.name, ErrNoModel)
	}
	system := t.cfg.System
	if run.Render != nil && system != "" {
		rendered, err := run.Render(system)
		if err != nil {
			return nil, fmt.Errorf("model %s: system: %w", t.name, err)
		}
		system = rendered
	}
	tools := make([]target.ChatTool, 0, len(t.cfg.Tools))
	byName := map[string]Tool{}
	for _, tl := range t.cfg.Tools {
		tools = append(tools, target.ChatTool{Name: tl.Name, Description: tl.Description, Parameters: tl.Parameters})
		byName[tl.Name] = tl
	}
	return &session{t: t, run: run, system: system, tools: tools, byName: byName, metrics: map[string]float64{}}, nil
}

type session struct {
	t      *Target
	run    *target.RunContext
	system string
	tools  []target.ChatTool
	byName map[string]Tool

	mu        sync.Mutex
	history   []target.ChatMessage
	pending   *target.UserTurn
	done      bool
	turns     int
	tokensIn  int
	tokensOut int
	toolCalls int
	metrics   map[string]float64
	evs       []target.Event
}

// Send implements target.Session: the user's text joins the conversation.
func (s *session) Send(_ context.Context, turn target.UserTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return errors.New("the session has ended")
	}
	if turn.Text == "" {
		return errors.New("the model target takes text turns")
	}
	s.history = append(s.history, target.ChatMessage{Role: "user", Text: turn.Text})
	s.pending = &turn
	return nil
}

// Receive implements target.Session: it calls the model, answers its tool
// calls from the configured results until it speaks, and returns the text.
func (s *session) Receive(ctx context.Context) (target.AgentTurn, error) {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if pending == nil {
		return target.AgentTurn{}, errors.New("Receive without a Send")
	}
	if err := ctx.Err(); err != nil {
		return target.AgentTurn{}, err
	}
	started := time.Now()
	out := target.AgentTurn{StartedAt: started}
	var raw []target.ChatResponse
	for round := 0; ; round++ {
		s.mu.Lock()
		req := target.ChatRequest{Model: s.t.cfg.Model, Fallback: s.t.cfg.Fallback, System: s.system, Messages: append([]target.ChatMessage(nil), s.history...), Tools: s.tools, Temperature: s.t.cfg.Temperature, MaxTokens: s.t.cfg.MaxTokens}
		s.mu.Unlock()
		resp, err := s.run.Chat.Chat(ctx, req)
		if err != nil {
			return out, fmt.Errorf("model %s: %w", s.t.name, err)
		}
		if resp.Model != "" && s.t.cfg.Model != "" && resp.Model != s.t.cfg.Model {
			s.mu.Lock()
			s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "model_fallback", Detail: resp.Model + " answered for " + s.t.cfg.Model})
			s.mu.Unlock()
		}
		if out.FirstTokenAt.IsZero() {
			out.FirstTokenAt = time.Now()
		}
		raw = append(raw, resp)
		s.mu.Lock()
		s.tokensIn += resp.InputTokens
		s.tokensOut += resp.OutputTokens
		modelMsg := target.ChatMessage{Role: "model", Text: resp.Text, ToolCalls: resp.ToolCalls}
		s.history = append(s.history, modelMsg)
		if resp.Text != "" {
			if out.Text != "" {
				out.Text += "\n"
			}
			out.Text += resp.Text
		}
		if len(resp.ToolCalls) == 0 {
			s.mu.Unlock()
			break
		}
		results := make([]target.ChatToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			s.toolCalls++
			out.ToolCalls = append(out.ToolCalls, target.ToolCall{Name: call.Name, Args: call.Args})
			s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "tool_call", Detail: call.Name + " " + compactJSON(call.Args)})
			if call.Name == s.t.cfg.DoneTool {
				s.done = true
				out.Done = true
				results = append(results, target.ChatToolResult{Ref: call.Ref, Name: call.Name, Output: map[string]any{"ok": true}})
				continue
			}
			tl, ok := s.byName[call.Name]
			switch {
			case !ok:
				results = append(results, target.ChatToolResult{Ref: call.Ref, Name: call.Name, Output: map[string]any{"error": "unknown tool " + call.Name}})
			case tl.Result == nil:
				results = append(results, target.ChatToolResult{Ref: call.Ref, Name: call.Name, Output: map[string]any{"error": "no result configured for " + call.Name}})
			default:
				results = append(results, target.ChatToolResult{Ref: call.Ref, Name: call.Name, Output: renderArgs(tl.Result, call.Args)})
			}
		}
		s.history = append(s.history, target.ChatMessage{Role: "tool", ToolResults: results})
		if out.Done || round+1 >= s.t.cfg.MaxToolRounds {
			if !out.Done {
				s.evs = append(s.evs, target.Event{At: time.Now(), Kind: "tool_rounds", Detail: fmt.Sprintf("stopped after %d tool rounds", s.t.cfg.MaxToolRounds)})
			}
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
	}
	out.EndedAt = time.Now()
	out.Raw = raw
	s.mu.Lock()
	s.turns++
	s.mu.Unlock()
	return out, nil
}

// Close implements target.Session.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	arts := target.Artifacts{Metrics: s.metrics, Files: map[string]string{}}
	arts.Metrics["turns"] = float64(s.turns)
	arts.Metrics["agent_input_tokens"] = float64(s.tokensIn)
	arts.Metrics["agent_output_tokens"] = float64(s.tokensOut)
	arts.Metrics["tool_calls"] = float64(s.toolCalls)
	arts.Events = s.evs
	return arts, nil
}

// renderArgs returns v with every {{args.<key>}} in its string leaves
// replaced by the call's argument; maps and slices are copied.
func renderArgs(v any, args map[string]any) any {
	switch x := v.(type) {
	case string:
		if !strings.Contains(x, "{{args.") {
			return x
		}
		out := x
		for k, a := range args {
			out = strings.ReplaceAll(out, "{{args."+k+"}}", argString(a))
		}
		return out
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = renderArgs(e, args)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = renderArgs(e, args)
		}
		return l
	}
	return v
}

func argString(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case nil:
		return ""
	}
	if b, err := json.Marshal(a); err == nil {
		return string(b)
	}
	return fmt.Sprint(a)
}

func compactJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
