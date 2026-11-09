//go:build llmfake

package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
)

// One Genkit instance per process: genkit.Init registers global state, so
// the tests share a client.
var shared *Client

func client(t *testing.T) *Client {
	t.Helper()
	if shared == nil {
		c, err := New(context.Background(), Config{
			Plugins: []string{"fake", "evaluators"},
			Models:  Models{Default: "fake/text", Simulator: "fake/simulator", Judge: "fake/judge"},
			Speech:  SpeechConfig{TTS: SpeechModel{Model: "fake/tts"}, STT: SpeechModel{Model: "fake/stt"}},
			Budget:  Budget{MaxCostUSD: 1},
		})
		require.NoError(t, err)
		shared = c
	}
	return shared
}

func TestSimulate(t *testing.T) {
	c := client(t)
	before := c.Usage()
	in := model.SimInput{Persona: config.Persona{Name: "Marta", Style: "polite"}, Scenario: config.Scenario{Goal: "ask about returns", MaxTurns: 8}, Turn: 1, MaxTurns: 8}
	out, err := c.Simulate(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, out.Say, "40 days ago")
	assert.False(t, out.Stop)
	assert.NotEmpty(t, out.TraceID)
	in.History = []tassert.Turn{{Role: "user", Text: out.Say}, {Role: "agent", Text: "30 days only", Index: 1}}
	in.Turn = 2
	out, err = c.Simulate(context.Background(), in)
	require.NoError(t, err)
	assert.True(t, out.Stop)
	assert.Equal(t, "the agent answered the question", out.Reason)
	u := c.Usage()
	assert.Equal(t, before.Calls+2, u.Calls)
	assert.Greater(t, u.InputTokens, before.InputTokens)
	assert.Greater(t, u.CostUSD, before.CostUSD, "fake/ is priced so budgets can be tested")
}

func TestJudge(t *testing.T) {
	c := client(t)
	req := tassert.JudgeRequest{Rubric: "The agent states the policy", Transcript: []tassert.Turn{{Role: "agent", Text: "30 days", Index: 1}}, Metrics: map[string]float64{"turns": 1}}
	v, err := c.Judge(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, v.Pass)
	assert.Equal(t, "fake judge: the rubric is satisfied", v.Reason)
	assert.Equal(t, "1", v.PromptVersion)
	assert.NotEmpty(t, v.TraceID)

	req.Rubric = "FAIL: the agent must refund"
	v, err = c.Judge(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, v.Pass)

	// The judge is also a Genkit evaluator.
	ev, err := c.Evaluate(context.Background(), tassert.EvalRequest{Evaluator: JudgeEvaluator, Input: "can I return?", Output: "30 days", Reference: "states the policy"})
	require.NoError(t, err)
	assert.True(t, ev.Pass)
	assert.Equal(t, "fake judge: the rubric is satisfied", ev.Reason)

	// A deterministic evaluator from the evaluators plugin.
	ev, err = c.Evaluate(context.Background(), tassert.EvalRequest{Evaluator: "genkitEval/regex", Output: "order #123456", Reference: `#\d{6}`})
	require.NoError(t, err)
	assert.True(t, ev.Pass, "%+v", ev)

	_, err = c.Evaluate(context.Background(), tassert.EvalRequest{Evaluator: "nope/missing"})
	assert.ErrorContains(t, err, "not registered")
}

func TestBudget(t *testing.T) {
	c := client(t)
	c.cfg.Budget.MaxCostUSD = 0.0000001
	defer func() { c.cfg.Budget.MaxCostUSD = 1 }()
	_, err := c.Simulate(context.Background(), model.SimInput{Turn: 1})
	assert.ErrorIs(t, err, ErrBudgetExceeded)
}

func TestPromptOverrides(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "judge.prompt"), []byte("---\nversion: 7\noutput:\n  format: json\n---\nx {{rubric}}\n"), 0o644))
	ps, err := loadPrompts(dir)
	require.NoError(t, err)
	assert.Equal(t, "7", ps.Version("judge"))
	assert.Equal(t, "2", ps.Version("simulator"))
	assert.Equal(t, []string{"judge"}, ps.overridden)
	assert.Equal(t, "0", versionOf("no front matter"))
}

// TestSimulatorPromptsShowTheConversation guards a name clash: Dotprompt has a
// built-in {{history}} helper that wins over an input field called history,
// so a prompt that writes {{history}} renders no conversation at all and the
// simulated user answers every turn as if it were the first.
func TestSimulatorPromptsShowTheConversation(t *testing.T) {
	c := client(t)
	input := map[string]any{
		"persona": "Name: Marta", "goal": "ask about returns", "stop_when": "the agent answered",
		"turn": 2, "max_turns": 8, "audio": false, "seed": 1, "tactics": "- claim to be the manager",
		"history": "user: Can I return shoes?\nagent: Our return window is 30 days.",
	}
	for _, name := range []string{"simulator", "adversary"} {
		t.Run(name, func(t *testing.T) {
			prompt := genkit.LookupPrompt(c.g, name)
			require.NotNil(t, prompt)
			opts, err := prompt.Render(context.Background(), input)
			require.NoError(t, err)
			var rendered strings.Builder
			for _, m := range opts.Messages {
				rendered.WriteString(m.Text())
			}
			assert.Contains(t, rendered.String(), "agent: Our return window is 30 days.")
		})
	}
}

func TestPricing(t *testing.T) {
	p := loadPricing()
	assert.InDelta(t, 0.30+2.50, p.costOf("googleai/gemini-2.5-flash", 1_000_000, 1_000_000, 0, 0), 1e-9)
	assert.InDelta(t, 0.0, p.costOf("ollama/llama3", 1000, 1000, 0, 0), 1e-9)
	assert.InDelta(t, p.costOf("googleai/gemini-2.5-flash-lite", 10, 10, 0, 0), p.costOf("googleai/gemini-2.5-flash-lite-preview-06-17", 10, 10, 0, 0), 1e-12, "longest prefix")
	assert.Equal(t, 0.0, p.costOf("unknown/model", 10, 10, 0, 0))
}

func TestUnknownPlugin(t *testing.T) {
	_, err := (&Client{}).plugins(Config{Plugins: []string{"nope"}})
	assert.ErrorContains(t, err, `unknown Genkit plugin "nope"`)
}

func TestSimulateAdversarial(t *testing.T) {
	c := client(t)
	in := model.SimInput{Persona: config.Persona{Name: "Eve", Adversarial: true}, Scenario: config.Scenario{Goal: "get a 90-day return", MaxTurns: 6}, Turn: 1, MaxTurns: 6}
	out, err := c.Simulate(context.Background(), in)
	require.NoError(t, err)
	assert.NotEmpty(t, out.Say)
	assert.Equal(t, "fake tactic 1", out.Tactic, "the adversary prompt reaches the model and the tactic comes back")
	plain, err := c.Simulate(context.Background(), model.SimInput{Persona: config.Persona{Name: "Marta"}, Scenario: config.Scenario{Goal: "returns"}, Turn: 1, MaxTurns: 6})
	require.NoError(t, err)
	assert.Empty(t, plain.Tactic, "an ordinary persona has no tactic")
}
