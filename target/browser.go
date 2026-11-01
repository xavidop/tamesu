package target

import (
	"context"
	"time"
)

// Observation is what the browser agent sees of a page: a pruned
// accessibility tree with numbered refs it acts on, the URL and title, the
// recent console errors and, when asked, a screenshot.
type Observation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	// Tree lists the interactive and informative nodes, one per line, as
	// `[ref] role "name" (state)`; refs are stable for a node across
	// observations of the same page.
	Tree string `json:"tree"`
	// Console holds the recent console errors and uncaught exceptions.
	Console []string `json:"console,omitempty"`
	// Screenshot is a PNG when the observation was taken with one.
	Screenshot []byte `json:"-"`
}

// Action is one browser action, as the agent requested it or as a
// deterministic step wrote it: the tool name and its arguments.
type Action struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// Page is the browser page a session drives: what the browser agent's tools
// and the deterministic steps call. Refs come from Observe.
type Page interface {
	URL(ctx context.Context) (string, error)
	// Observe returns the current page state; withScreenshot adds a PNG.
	Observe(ctx context.Context, withScreenshot bool) (Observation, error)
	Click(ctx context.Context, ref int) error
	// Type focuses the node, clears it and types text (secrets are rendered
	// here, never shown to a model).
	Type(ctx context.Context, ref int, text string) error
	Select(ctx context.Context, ref int, value string) error
	// Scroll is up, down, top or bottom.
	Scroll(ctx context.Context, direction string) error
	// Navigate loads url, refusing a host outside the allowed domains.
	Navigate(ctx context.Context, url string) error
	// Press sends a key such as Enter, Tab or Escape to the focused node.
	Press(ctx context.Context, key string) error
	Wait(ctx context.Context, d time.Duration) error
	Screenshot(ctx context.Context) ([]byte, error)
	// Eval runs JavaScript and returns its JSON value.
	Eval(ctx context.Context, js string) (any, error)
	// Text is the visible text of the page.
	Text(ctx context.Context) (string, error)
	// Do runs one deterministic action (navigate, click, fill, press, wait,
	// screenshot, eval_js, assert_url, assert_text) and describes what it did;
	// a failed assert_* is a StepError.
	Do(ctx context.Context, action map[string]any) (string, error)
}

// StepError is a deterministic step whose check did not hold (assert_url,
// assert_text); the runner records it as a failed step, not an error.
type StepError struct{ Reason string }

func (e *StepError) Error() string { return e.Reason }
