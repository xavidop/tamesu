//go:build llmfake

package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/llm/llmfake"
	"github.com/xavidop/tamesu/target"
)

// fakePage is a page with a login form; typing a user and clicking the button
// logs in.
type fakePage struct {
	user, pass string
	loggedIn   bool
	actions    []string
	url        string
}

func (p *fakePage) URL(context.Context) (string, error) { return p.url, nil }
func (p *fakePage) Observe(context.Context, bool) (target.Observation, error) {
	tree := "[1] textbox \"Username\"\n[2] textbox \"Password\"\n[3] button \"Log in\""
	if p.loggedIn {
		tree = "    heading \"Welcome back, " + p.user + "\" (h1)\n[4] link \"Cart\""
	}
	return target.Observation{URL: p.url, Title: "Shop", Tree: tree}, nil
}
func (p *fakePage) Click(_ context.Context, ref int) error {
	p.actions = append(p.actions, "click")
	if ref == 3 && p.user != "" {
		p.loggedIn = true
	}
	return nil
}
func (p *fakePage) Type(_ context.Context, ref int, text string) error {
	p.actions = append(p.actions, "type")
	switch ref {
	case 1:
		p.user = text
	case 2:
		p.pass = text
	}
	return nil
}
func (p *fakePage) Select(context.Context, int, string) error {
	p.actions = append(p.actions, "select")
	return nil
}
func (p *fakePage) Scroll(context.Context, string) error {
	p.actions = append(p.actions, "scroll")
	return nil
}
func (p *fakePage) Navigate(_ context.Context, u string) error {
	p.actions = append(p.actions, "navigate")
	p.url = u
	return nil
}
func (p *fakePage) Press(context.Context, string) error                { return nil }
func (p *fakePage) Wait(context.Context, time.Duration) error          { return nil }
func (p *fakePage) Screenshot(context.Context) ([]byte, error)         { return []byte("\x89PNG"), nil }
func (p *fakePage) Eval(context.Context, string) (any, error)          { return nil, nil }
func (p *fakePage) Text(context.Context) (string, error)               { return "", nil }
func (p *fakePage) Do(context.Context, map[string]any) (string, error) { return "", nil }

func TestRunGoalWithScriptedTools(t *testing.T) {
	c := client(t)
	saved := c.cfg.Models.BrowserAgent
	c.cfg.Models.BrowserAgent = "fake/browser"
	defer func() { c.cfg.Models.BrowserAgent = saved }()
	fakePlugin().(*llmfake.Plugin).SetScript(llmfake.Script{Browser: []llmfake.BrowserAction{
		{Tool: "type", Input: map[string]any{"find": `"Username"`, "text": "marta"}},
		{Tool: "type", Input: map[string]any{"find": `"Password"`, "text": "{{secrets.TEST_PASSWORD}}"}},
		{Tool: "click", Input: map[string]any{"find": `"Log in"`}},
		{Tool: "done", Input: map[string]any{"success": true, "reason": "the page greets marta"}},
	}})
	defer fakePlugin().(*llmfake.Plugin).SetScript(llmfake.DefaultScript)

	page := &fakePage{url: "http://shop/"}
	res, err := c.RunGoal(context.Background(), model.GoalRequest{Goal: "Log in as marta with password {{secrets.TEST_PASSWORD}}", Page: page, MaxSteps: 6, AllowedDomains: []string{"shop"}})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "the page greets marta", res.Reason)
	assert.Equal(t, 3, res.Steps, "done is not an action")
	assert.Equal(t, []string{"type", "type", "click"}, page.actions)
	assert.True(t, page.loggedIn)
	assert.Equal(t, "{{secrets.TEST_PASSWORD}}", page.pass, "the placeholder reaches the page untouched; the page renders it")
	require.Len(t, res.Actions, 3)
	assert.Equal(t, "type", res.Actions[0].Name)
	assert.Equal(t, 1, res.Actions[0].Args["ref"])
	assert.NotEmpty(t, res.TraceID)

	// A goal whose script never calls done ends with a failure reason.
	fakePlugin().(*llmfake.Plugin).SetScript(llmfake.Script{Browser: []llmfake.BrowserAction{{Tool: "scroll", Input: map[string]any{"direction": "down"}}}})
	res, err = c.RunGoal(context.Background(), model.GoalRequest{Goal: "scroll a bit", Page: &fakePage{}, MaxSteps: 4})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Reason, "without calling done")
	assert.Equal(t, 1, res.Steps)
}

func TestCheckExpect(t *testing.T) {
	c := client(t)
	saved := c.cfg.Models.BrowserAgent
	c.cfg.Models.BrowserAgent = "fake/expect"
	defer func() { c.cfg.Models.BrowserAgent = saved }()
	res, err := c.CheckExpect(context.Background(), &fakePage{url: "http://shop/cart"}, "The cart shows one item")
	require.NoError(t, err)
	assert.True(t, res.Pass)
	assert.Contains(t, res.Reason, "fake expect")
	res, err = c.CheckExpect(context.Background(), &fakePage{}, "FAIL: the cart is empty")
	require.NoError(t, err)
	assert.False(t, res.Pass)
	assert.NotEmpty(t, res.TraceID)
}

func TestFormatObservation(t *testing.T) {
	s := formatObservation(target.Observation{URL: "http://x/", Title: "T", Tree: "[1] button \"Go\"", Console: []string{"err: boom"}})
	assert.True(t, strings.HasPrefix(s, "URL: http://x/\nTitle: T\n"))
	assert.Contains(t, s, "Console errors:\n  err: boom")
	assert.Contains(t, s, "Elements:\n[1] button \"Go\"")
	assert.Contains(t, formatObservation(target.Observation{}), "(nothing visible)")
}
