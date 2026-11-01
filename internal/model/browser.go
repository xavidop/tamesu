package model

import (
	"context"

	"github.com/xavidop/tamesu/target"
)

// GoalRequest asks the browser agent to achieve a goal on a page.
type GoalRequest struct {
	Goal string
	Page target.Page
	// MaxSteps caps the tool calls (default 15).
	MaxSteps int
	// Screenshots sends a PNG with each observation when the model takes media.
	Screenshots bool
	// AllowedDomains is told to the agent; the page enforces it.
	AllowedDomains []string
	// Model overrides models.browser_agent.
	Model string
}

// GoalResult is what the agent reported.
type GoalResult struct {
	Success bool
	Reason  string
	// Steps is the number of actions taken; Actions lists them.
	Steps   int
	Actions []target.Action
	TraceID string
}

// ExpectResult is the verdict of an expect step.
type ExpectResult struct {
	Pass    bool
	Reason  string
	TraceID string
}

// BrowserAgent plays the user in a browser test: the browserStep and
// checkExpect flows. The llm module implements it.
type BrowserAgent interface {
	RunGoal(ctx context.Context, req GoalRequest) (GoalResult, error)
	CheckExpect(ctx context.Context, page target.Page, expectation string) (ExpectResult, error)
}
