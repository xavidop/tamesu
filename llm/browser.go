package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// Browser flow names and defaults.
const (
	FlowBrowserStep  = "browserStep"
	FlowCheckExpect  = "checkExpect"
	DefaultMaxSteps  = 15
	DefaultStepPause = 300 * time.Millisecond
)

// Tool names the browser agent calls.
var BrowserTools = []string{"click", "type", "select", "scroll", "navigate", "wait", "done"}

var _ model.BrowserAgent = (*Client)(nil)

// goalState is what the tools of one goal share: the page and the outcome.
type goalState struct {
	page    target.Page
	actions []target.Action
	done    *model.GoalResult
	steps   int
	// tree is the last observation's element list, to name the element a
	// ref points at (so plans can be replayed on a fresh page).
	tree string
}

type goalKey struct{}

func stateFrom(ctx context.Context) (*goalState, error) {
	st, _ := ctx.Value(goalKey{}).(*goalState)
	if st == nil {
		return nil, errors.New("llm: browser tool called outside a goal")
	}
	return st, nil
}

// goalInput is the browserStep flow's input; the page travels in the context.
type goalInput struct {
	Goal           string   `json:"goal"`
	MaxSteps       int      `json:"max_steps"`
	Screenshots    bool     `json:"screenshots,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	Model          string   `json:"model,omitempty"`
}

type goalOutput struct {
	Success bool            `json:"success"`
	Reason  string          `json:"reason"`
	Steps   int             `json:"steps"`
	Actions []target.Action `json:"actions,omitempty"`
	TraceID string          `json:"trace_id,omitempty"`
}

type expectInput struct {
	Expectation string `json:"expectation"`
	Model       string `json:"model,omitempty"`
}

type expectOutput struct {
	Pass    bool   `json:"pass"`
	Reason  string `json:"reason"`
	TraceID string `json:"trace_id,omitempty"`
}

// Tool inputs.
type refInput struct {
	Ref int `json:"ref" jsonschema:"description=The [ref] number of the element."`
}

type typeInput struct {
	Ref  int    `json:"ref" jsonschema:"description=The [ref] number of the field."`
	Text string `json:"text" jsonschema:"description=What to type; a {{secrets.NAME}} placeholder is typed literally."`
}

type selectInput struct {
	Ref   int    `json:"ref" jsonschema:"description=The [ref] number of the select."`
	Value string `json:"value" jsonschema:"description=The option's value or visible label."`
}

type scrollInput struct {
	Direction string `json:"direction" jsonschema:"description=up, down, top or bottom."`
}

type navigateInput struct {
	URL string `json:"url" jsonschema:"description=An absolute URL on the application's domain."`
}

type waitInput struct {
	Ms int `json:"ms" jsonschema:"description=Milliseconds to wait, at most 10000."`
}

type doneInput struct {
	Success bool   `json:"success" jsonschema:"description=Whether the goal was achieved."`
	Reason  string `json:"reason" jsonschema:"description=One sentence naming what on the page shows it."`
}

// defineBrowser registers the browser tools and flows.
func (c *Client) defineBrowser() {
	c.tools = []ai.ToolRef{
		genkit.DefineTool(c.g, "click", "Click the element with this ref.", func(ctx *ai.ToolContext, in refInput) (string, error) {
			return c.act(ctx, "click", map[string]any{"ref": in.Ref}, func(st *goalState) error { return st.page.Click(ctx, in.Ref) })
		}),
		genkit.DefineTool(c.g, "type", "Clear the field with this ref and type the text into it.", func(ctx *ai.ToolContext, in typeInput) (string, error) {
			return c.act(ctx, "type", map[string]any{"ref": in.Ref, "text": in.Text}, func(st *goalState) error { return st.page.Type(ctx, in.Ref, in.Text) })
		}),
		genkit.DefineTool(c.g, "select", "Pick an option of the select with this ref, by value or label.", func(ctx *ai.ToolContext, in selectInput) (string, error) {
			return c.act(ctx, "select", map[string]any{"ref": in.Ref, "value": in.Value}, func(st *goalState) error { return st.page.Select(ctx, in.Ref, in.Value) })
		}),
		genkit.DefineTool(c.g, "scroll", "Scroll the page: up, down, top or bottom.", func(ctx *ai.ToolContext, in scrollInput) (string, error) {
			return c.act(ctx, "scroll", map[string]any{"direction": in.Direction}, func(st *goalState) error { return st.page.Scroll(ctx, in.Direction) })
		}),
		genkit.DefineTool(c.g, "navigate", "Load a URL on the application's own domain.", func(ctx *ai.ToolContext, in navigateInput) (string, error) {
			return c.act(ctx, "navigate", map[string]any{"url": in.URL}, func(st *goalState) error { return st.page.Navigate(ctx, in.URL) })
		}),
		genkit.DefineTool(c.g, "wait", "Wait for the page to change, in milliseconds (at most 10000).", func(ctx *ai.ToolContext, in waitInput) (string, error) {
			ms := min(max(in.Ms, 0), 10000)
			return c.act(ctx, "wait", map[string]any{"ms": ms}, func(st *goalState) error { return st.page.Wait(ctx, time.Duration(ms)*time.Millisecond) })
		}),
		genkit.DefineTool(c.g, "done", "Finish: the goal is achieved (success true) or cannot be (false), with the reason.", func(ctx *ai.ToolContext, in doneInput) (string, error) {
			st, err := stateFrom(ctx)
			if err != nil {
				return "", err
			}
			st.done = &model.GoalResult{Success: in.Success, Reason: in.Reason}
			return "Recorded. Reply with one short sentence summarising the outcome.", nil
		}),
	}
	c.goalFlow = genkit.DefineFlow(c.g, FlowBrowserStep, c.browserStep)
	c.expectFlow = genkit.DefineFlow(c.g, FlowCheckExpect, c.checkExpect)
}

// act runs one page action for a tool and answers with the new page.
func (c *Client) act(ctx *ai.ToolContext, name string, args map[string]any, do func(*goalState) error) (string, error) {
	st, err := stateFrom(ctx)
	if err != nil {
		return "", err
	}
	st.steps++
	if ref, ok := args["ref"].(int); ok {
		if el := elementLine(st.tree, ref); el != "" {
			args["element"] = el
		}
	}
	st.actions = append(st.actions, target.Action{Name: name, Args: args})
	if err := do(st); err != nil {
		// The model hears about the failure and may try another way.
		obs, oerr := st.page.Observe(ctx, false)
		if oerr != nil {
			return fmt.Sprintf("%s failed: %v", name, err), nil
		}
		st.tree = obs.Tree
		return fmt.Sprintf("%s failed: %v\n\nCurrent page:\n%s", name, err, formatObservation(obs)), nil
	}
	_ = st.page.Wait(ctx, DefaultStepPause)
	obs, err := st.page.Observe(ctx, false)
	if err != nil {
		return "", err
	}
	st.tree = obs.Tree
	return "Done. Current page:\n" + formatObservation(obs), nil
}

// RunGoal implements model.BrowserAgent through the browserStep flow.
func (c *Client) RunGoal(ctx context.Context, req model.GoalRequest) (model.GoalResult, error) {
	if c.overBudget() {
		return model.GoalResult{}, ErrBudgetExceeded
	}
	if req.Page == nil {
		return model.GoalResult{}, errors.New("llm: RunGoal needs a page")
	}
	st := &goalState{page: req.Page}
	out, err := c.goalFlow.Run(context.WithValue(ctx, goalKey{}, st), goalInput{Goal: req.Goal, MaxSteps: req.MaxSteps, Screenshots: req.Screenshots, AllowedDomains: req.AllowedDomains, Model: req.Model})
	if err != nil {
		return model.GoalResult{Steps: st.steps, Actions: st.actions}, err
	}
	return model.GoalResult{Success: out.Success, Reason: out.Reason, Steps: out.Steps, Actions: out.Actions, TraceID: out.TraceID}, nil
}

func (c *Client) browserStep(ctx context.Context, in goalInput) (goalOutput, error) {
	st, err := stateFrom(ctx)
	if err != nil {
		return goalOutput{}, err
	}
	prompt := genkit.LookupPrompt(c.g, "browser_agent")
	if prompt == nil {
		return goalOutput{}, fmt.Errorf("llm: prompt browser_agent is not loaded")
	}
	maxSteps := in.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	// A retry or a fallback observes the page again: the steps already taken
	// stand, and the next model carries on from where the page is.
	models := chain(c.modelFor("browser_agent", in.Model), c.fallbacksFor("browser_agent"))
	return attempt(ctx, c, "browser_agent", models, func(ctx context.Context, modelName string) (goalOutput, error) {
		obs, err := st.page.Observe(ctx, in.Screenshots)
		if err != nil {
			return goalOutput{}, fmt.Errorf("llm: %s: observe: %w", FlowBrowserStep, err)
		}
		st.tree = obs.Tree
		input := map[string]any{
			"goal": in.Goal, "max_steps": maxSteps, "observation": formatObservation(obs),
			"allowed_domains": strings.Join(in.AllowedDomains, ", "), "open": "{{", "close": "}}",
		}
		opts := []ai.PromptExecuteOption{ai.WithInput(input), ai.WithModelName(modelName), ai.WithTools(c.tools...), ai.WithMaxTurns(maxSteps + 2)}
		if in.Screenshots && len(obs.Screenshot) > 0 {
			shot := &ai.Document{Content: []*ai.Part{ai.NewMediaPart("image/png", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(obs.Screenshot))}, Metadata: map[string]any{"name": "screenshot"}}
			opts = append(opts, ai.WithDocs(shot))
		}
		resp, err := prompt.Execute(ctx, opts...)
		budgetErr := c.record(modelName, resp, err)
		out := goalOutput{Steps: st.steps, Actions: st.actions, TraceID: tracing.SpanTraceInfo(ctx).TraceID}
		if err != nil {
			if st.done != nil {
				// done was called and the loop ended on the step budget: keep the verdict.
				out.Success, out.Reason = st.done.Success, st.done.Reason
				return out, nil
			}
			return out, fmt.Errorf("llm: %s with %s: %w", FlowBrowserStep, modelName, err)
		}
		switch {
		case st.done != nil:
			out.Success, out.Reason = st.done.Success, st.done.Reason
		default:
			out.Success = false
			out.Reason = "the agent stopped without calling done: " + short(resp.Text())
			if st.steps >= maxSteps {
				out.Reason = fmt.Sprintf("the agent used its %d steps without reaching the goal", maxSteps)
			}
		}
		return out, budgetErr
	})
}

// CheckExpect implements model.BrowserAgent through the checkExpect flow.
func (c *Client) CheckExpect(ctx context.Context, page target.Page, expectation string) (model.ExpectResult, error) {
	if c.overBudget() {
		return model.ExpectResult{}, ErrBudgetExceeded
	}
	st := &goalState{page: page}
	out, err := c.expectFlow.Run(context.WithValue(ctx, goalKey{}, st), expectInput{Expectation: expectation})
	if err != nil {
		return model.ExpectResult{}, err
	}
	return model.ExpectResult{Pass: out.Pass, Reason: out.Reason, TraceID: out.TraceID}, nil
}

func (c *Client) checkExpect(ctx context.Context, in expectInput) (expectOutput, error) {
	st, err := stateFrom(ctx)
	if err != nil {
		return expectOutput{}, err
	}
	obs, err := st.page.Observe(ctx, false)
	if err != nil {
		return expectOutput{}, fmt.Errorf("llm: %s: observe: %w", FlowCheckExpect, err)
	}
	input := map[string]any{"expectation": in.Expectation, "observation": formatObservation(obs)}
	models := chain(c.modelFor("browser_agent", in.Model), c.fallbacksFor("browser_agent"))
	return attempt(ctx, c, "browser_agent", models, func(ctx context.Context, modelName string) (expectOutput, error) {
		resp, err := c.executePrompt(ctx, "expect", input, modelName)
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return expectOutput{}, fmt.Errorf("llm: %s with %s: %w", FlowCheckExpect, modelName, err)
		}
		var out expectOutput
		if err := resp.Output(&out); err != nil {
			return expectOutput{}, badOutput(FlowCheckExpect, modelName, err, resp.Text())
		}
		out.TraceID = tracing.SpanTraceInfo(ctx).TraceID
		return out, budgetErr
	})
}

// elementLine returns the `role "name"` of a ref's line in the tree.
func elementLine(tree string, ref int) string {
	prefix := fmt.Sprintf("[%d] ", ref)
	for _, line := range strings.Split(tree, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		rest := strings.TrimSpace(t[len(prefix):])
		if i := strings.Index(rest, `" value=`); i >= 0 {
			rest = rest[:i+1]
		}
		if i := strings.Index(rest, `" (`); i >= 0 {
			rest = rest[:i+1]
		}
		if !strings.Contains(rest, `"`) {
			if i := strings.Index(rest, " ("); i >= 0 {
				rest = rest[:i]
			}
		}
		return rest
	}
	return ""
}

// formatObservation renders an observation for a prompt.
func formatObservation(obs target.Observation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nTitle: %s\n", obs.URL, obs.Title)
	if len(obs.Console) > 0 {
		b.WriteString("Console errors:\n")
		for _, c := range obs.Console {
			b.WriteString("  " + c + "\n")
		}
	}
	b.WriteString("Elements:\n")
	if obs.Tree == "" {
		b.WriteString("  (nothing visible)\n")
	} else {
		b.WriteString(obs.Tree)
	}
	return b.String()
}
