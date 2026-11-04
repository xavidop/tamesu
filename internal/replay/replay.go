// Package replay caches the simulator's turns and the judge's verdicts on
// disk, keyed by the request, so a repeated run with the same conversation
// costs nothing and is deterministic. Only the conversational text and
// metadata go into a key; metrics and timestamps do not.
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xavidop/tamesu/internal/fsutil"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// Mode is how the cache is used.
type Mode string

const (
	// ModeAuto answers from the cache and calls the model on a miss, storing
	// the answer.
	ModeAuto Mode = "auto"
	// ModeReplay answers simulator turns from the cache only, since a fresh
	// turn would change the conversation; a judge miss still calls the
	// model, because a verdict changes nothing but the result.
	ModeReplay Mode = "replay"
	// ModeUpdate ignores the cache, calls the model and refreshes it.
	ModeUpdate Mode = "update"
	// ModeOff passes every call through.
	ModeOff Mode = "off"
)

// ErrMiss is returned in ModeReplay when a request is not cached.
var ErrMiss = errors.New("replay: no cached answer for this request (run without --replay to record one)")

// Provider wraps a model.Provider with the cache. It forwards the speech,
// browser agent and generator roles when the inner provider has them.
type Provider struct {
	inner  model.Provider
	dir    string
	mode   Mode
	hits   atomic.Int64
	misses atomic.Int64
}

// Stats are the cache's counters.
type Stats struct {
	Hits   int `json:"hits"`
	Misses int `json:"misses"`
}

// New wraps inner with the cache under dir/llm.
func New(inner model.Provider, dir string, mode Mode) *Provider {
	if mode == "" {
		mode = ModeAuto
	}
	return &Provider{inner: inner, dir: filepath.Join(dir, "llm"), mode: mode}
}

// Stats returns the hits and misses so far.
func (p *Provider) Stats() Stats {
	return Stats{Hits: int(p.hits.Load()), Misses: int(p.misses.Load())}
}

// Inner returns the wrapped provider.
func (p *Provider) Inner() model.Provider { return p.inner }

type simEntry struct {
	Kind      string                `json:"kind"`
	CreatedAt time.Time             `json:"created_at"`
	Request   json.RawMessage       `json:"request"`
	Sim       *model.SimOutput      `json:"sim,omitempty"`
	Judge     *assert.JudgeVerdict  `json:"judge,omitempty"`
	Ground    *assert.GroundVerdict `json:"ground,omitempty"`
	Extract   *model.ExtractResult  `json:"extract,omitempty"`
}

// simKey is what identifies a simulator turn: who is talking, about what,
// the conversation so far and the turn number.
type simKey struct {
	Persona  any        `json:"persona"`
	Scenario any        `json:"scenario"`
	History  []turnText `json:"history"`
	Turn     int        `json:"turn"`
	MaxTurns int        `json:"max_turns"`
	Mode     string     `json:"mode"`
	Seed     int64      `json:"seed"`
}

type turnText struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// judgeKey is what identifies a verdict: the rubric, the scale, the model
// and the conversation; metrics vary between identical conversations and
// stay out.
type judgeKey struct {
	Rubric     string            `json:"rubric"`
	Scale      string            `json:"scale"`
	Min        float64           `json:"min"`
	Model      string            `json:"model"`
	Transcript []turnText        `json:"transcript"`
	ToolCalls  []target.ToolCall `json:"tool_calls"`
}

func turns(ts []assert.Turn) []turnText {
	out := make([]turnText, len(ts))
	for i, t := range ts {
		out[i] = turnText{Role: t.Role, Text: t.Text}
	}
	return out
}

func keyOf(kind string, v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(append([]byte(kind+":"), b...))
	return kind + "-" + hex.EncodeToString(h[:16]), b, nil
}

func (p *Provider) load(key string) (*simEntry, bool) {
	if p.mode == ModeOff || p.mode == ModeUpdate {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(p.dir, key+".json"))
	if err != nil {
		return nil, false
	}
	var e simEntry
	if json.Unmarshal(b, &e) != nil {
		return nil, false
	}
	return &e, true
}

func (p *Provider) store(key string, e simEntry) {
	if p.mode == ModeOff {
		return
	}
	e.CreatedAt = time.Now()
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return
	}
	// Atomic: parallel runs that miss the same key must not leave a torn file.
	_ = fsutil.WriteFileAtomic(filepath.Join(p.dir, key+".json"), b, 0o644)
}

// Simulate answers from the cache or the inner simulator.
func (p *Provider) Simulate(ctx context.Context, in model.SimInput) (model.SimOutput, error) {
	key, raw, err := keyOf("sim", simKey{Persona: in.Persona, Scenario: in.Scenario, History: turns(in.History), Turn: in.Turn, MaxTurns: in.MaxTurns, Mode: in.Mode, Seed: in.Seed})
	if err != nil {
		return p.inner.Simulate(ctx, in)
	}
	if e, ok := p.load(key); ok && e.Sim != nil {
		p.hits.Add(1)
		return *e.Sim, nil
	}
	if p.mode == ModeReplay {
		return model.SimOutput{}, fmt.Errorf("simulator turn %d: %w", in.Turn, ErrMiss)
	}
	p.misses.Add(1)
	out, err := p.inner.Simulate(ctx, in)
	if err != nil {
		return out, err
	}
	p.store(key, simEntry{Kind: "sim", Request: raw, Sim: &out})
	return out, nil
}

// Judge answers from the cache or the inner judge.
func (p *Provider) Judge(ctx context.Context, req assert.JudgeRequest) (assert.JudgeVerdict, error) {
	key, raw, err := keyOf("judge", judgeKey{Rubric: req.Rubric, Scale: req.Scale, Min: req.Min, Model: req.Model, Transcript: turns(req.Transcript), ToolCalls: req.ToolCalls})
	if err != nil {
		return p.inner.Judge(ctx, req)
	}
	if e, ok := p.load(key); ok && e.Judge != nil {
		p.hits.Add(1)
		return *e.Judge, nil
	}
	p.misses.Add(1)
	out, err := p.inner.Judge(ctx, req)
	if err != nil {
		return out, err
	}
	p.store(key, simEntry{Kind: "judge", Request: raw, Judge: &out})
	return out, nil
}

// Ground answers from the cache or the inner grounder.
func (p *Provider) Ground(ctx context.Context, req assert.GroundRequest) (assert.GroundVerdict, error) {
	key, raw, err := keyOf("ground", map[string]any{"context": req.Context, "model": req.Model, "transcript": turns(req.Transcript)})
	if err != nil {
		return p.inner.Ground(ctx, req)
	}
	if e, ok := p.load(key); ok && e.Ground != nil {
		p.hits.Add(1)
		return *e.Ground, nil
	}
	p.misses.Add(1)
	out, err := p.inner.Ground(ctx, req)
	if err != nil {
		return out, err
	}
	p.store(key, simEntry{Kind: "ground", Request: raw, Ground: &out})
	return out, nil
}

// Extract answers from the cache or the inner extractor.
func (p *Provider) Extract(ctx context.Context, req model.ExtractRequest) (model.ExtractResult, error) {
	key, raw, err := keyOf("extract", req)
	if err != nil {
		return p.inner.Extract(ctx, req)
	}
	if e, ok := p.load(key); ok && e.Extract != nil {
		p.hits.Add(1)
		return *e.Extract, nil
	}
	p.misses.Add(1)
	out, err := p.inner.Extract(ctx, req)
	if err != nil {
		return out, err
	}
	p.store(key, simEntry{Kind: "extract", Request: raw, Extract: &out})
	return out, nil
}

// Evaluate passes through: evaluators are deterministic or cheap.
func (p *Provider) Evaluate(ctx context.Context, req assert.EvalRequest) (assert.EvalVerdict, error) {
	return p.inner.Evaluate(ctx, req)
}

// Usage passes through.
func (p *Provider) Usage() model.Usage { return p.inner.Usage() }

// Close passes through.
func (p *Provider) Close(ctx context.Context) error { return p.inner.Close(ctx) }

// Synthesize forwards to the inner provider's speech, when it has one.
func (p *Provider) Synthesize(ctx context.Context, req target.SynthesizeRequest) (target.Audio, error) {
	if sp, ok := p.inner.(target.Speech); ok {
		return sp.Synthesize(ctx, req)
	}
	return target.Audio{}, errors.New("replay: the provider has no speech models")
}

// Transcribe forwards to the inner provider's speech, when it has one.
func (p *Provider) Transcribe(ctx context.Context, req target.TranscribeRequest) (target.Transcript, error) {
	if sp, ok := p.inner.(target.Speech); ok {
		return sp.Transcribe(ctx, req)
	}
	return target.Transcript{}, errors.New("replay: the provider has no speech models")
}

// Chat forwards to the inner provider's chat model (the model target). Its
// answers are the agent's, so they are never cached: a cached answer would
// hide the very change the suite tests.
func (p *Provider) Chat(ctx context.Context, req target.ChatRequest) (target.ChatResponse, error) {
	if cm, ok := p.inner.(target.ChatModel); ok {
		return cm.Chat(ctx, req)
	}
	return target.ChatResponse{}, errors.New("replay: the provider has no chat model")
}

// Embed forwards to the inner embedder. Embeddings are not cached: they are
// cheap, and a similar_to assertion compares the agent's actual words.
func (p *Provider) Embed(ctx context.Context, req assert.EmbedRequest) (assert.EmbedResponse, error) {
	if e, ok := p.inner.(assert.Embedder); ok {
		return e.Embed(ctx, req)
	}
	return assert.EmbedResponse{}, errors.New("replay: the provider has no embedding model")
}

// HasSpeech reports whether the inner provider has speech models.
func (p *Provider) HasSpeech() bool { _, ok := p.inner.(target.Speech); return ok }

// RunGoal forwards to the inner browser agent.
func (p *Provider) RunGoal(ctx context.Context, req model.GoalRequest) (model.GoalResult, error) {
	if ba, ok := p.inner.(model.BrowserAgent); ok {
		return ba.RunGoal(ctx, req)
	}
	return model.GoalResult{}, errors.New("replay: the provider has no browser agent")
}

// CheckExpect forwards to the inner browser agent.
func (p *Provider) CheckExpect(ctx context.Context, page target.Page, expectation string) (model.ExpectResult, error) {
	if ba, ok := p.inner.(model.BrowserAgent); ok {
		return ba.CheckExpect(ctx, page, expectation)
	}
	return model.ExpectResult{}, errors.New("replay: the provider has no browser agent")
}

// GenerateTests forwards to the inner generator.
func (p *Provider) GenerateTests(ctx context.Context, req model.GenerateRequest) (model.Draft, error) {
	if g, ok := p.inner.(model.Generator); ok {
		return g.GenerateTests(ctx, req)
	}
	return model.Draft{}, errors.New("replay: the provider has no scenario generator")
}
