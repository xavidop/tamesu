package model

import "context"

// GenerateRequest asks for draft tests from a document.
type GenerateRequest struct {
	// Source is the PRD, system prompt or knowledge base text.
	Source string
	// SourceName names it (a file name), for the suite name.
	SourceName string
	// Target is the target's name in the suite and Type its type (rest,
	// livekit, sip, browser).
	Target string
	Type   string
	// Count is how many tests to draft (default 5).
	Count int
	// Model overrides models.scenario_generator.
	Model string
}

// DraftTest is one generated test, before it becomes YAML.
type DraftTest struct {
	Name        string   `json:"name"`
	Tags        []string `json:"tags,omitempty"`
	Persona     string   `json:"persona,omitempty"`
	Style       string   `json:"style,omitempty"`
	Language    string   `json:"language,omitempty"`
	Goal        string   `json:"goal,omitempty"`
	MaxTurns    int      `json:"max_turns,omitempty"`
	StopWhen    string   `json:"stop_when,omitempty"`
	Judges      []string `json:"judges,omitempty"`
	Contains    []string `json:"contains,omitempty"`
	NotContains []string `json:"not_contains,omitempty"`
	// Browser tests: goal sentences in order, then expectations.
	Steps   []string `json:"steps,omitempty"`
	Expects []string `json:"expects,omitempty"`
}

// Draft is the generator's answer.
type Draft struct {
	Tests   []DraftTest `json:"tests"`
	TraceID string      `json:"trace_id,omitempty"`
}

// Generator drafts tests from a document (the generateScenarios flow). The
// llm module implements it.
type Generator interface {
	GenerateTests(ctx context.Context, req GenerateRequest) (Draft, error)
}
