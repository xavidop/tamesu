// Package llm is the only place tamesu calls a model. Every call goes through
// Genkit: the simulated user (simulateUserTurn), the judges (judgeRun),
// Genkit evaluators, speech (synthesize, transcribe) and the browser agent
// (browserStep, checkExpect). Prompts are dotprompt files embedded
// from llm/prompts and overridable from .tamesu/prompts; every model is a
// Genkit reference such as googleai/gemini-2.5-flash, so switching provider
// is a config change. Usage and cost are counted on every response.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
)

// ErrBudgetExceeded is returned by every call once the configured budget is spent.
var ErrBudgetExceeded = errors.New("llm: cost budget exceeded")

// Models names the Genkit model for each job (design.md §4 models:).
type Models struct {
	Default           string
	Judge             string
	Simulator         string
	BrowserAgent      string
	ScenarioGenerator string
}

// Budget caps tamesu's own spend; zero means unlimited.
type Budget struct {
	MaxCostUSD float64
	MaxTokens  int
}

// Config configures the client.
type Config struct {
	// Plugins to initialise by name: googlegenai, anthropic, openai, ollama,
	// evaluators, and fake (only in a build with -tags llmfake).
	Plugins []string
	Models  Models
	// Credentials returns a resolved secret by conventional name
	// (GEMINI_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, ...). It is called
	// at plugin initialisation only. nil means the plugins fall back to the
	// process environment.
	Credentials func(name string) (string, bool)
	// OllamaURL is the Ollama server (default http://localhost:11434).
	OllamaURL string
	// PromptOverrideDir holds .prompt files that replace the embedded ones by
	// name (default .tamesu/prompts, when it exists).
	PromptOverrideDir string
	// DevUI exports traces to the Genkit Developer UI (GENKIT_ENV=dev).
	DevUI  bool
	Budget Budget
	// Speech selects the TTS and STT models of audio mode.
	Speech SpeechConfig
	Logger *slog.Logger
}

// Option configures New beyond Config.
type Option func(*Client)

// WithGenkit reuses the caller's Genkit instance (its plugins, models and
// tracing) instead of initialising one: how a Genkit app tests its own agent
// in-process. Config.Plugins is then ignored.
func WithGenkit(g *genkit.Genkit) Option { return func(c *Client) { c.g = g } }

// Client is the AI layer of a run. It implements model.Provider.
type Client struct {
	g       *genkit.Genkit
	cfg     Config
	logger  *slog.Logger
	prompts *promptSet
	pricing pricing

	mu    sync.Mutex
	usage model.Usage

	simFlow      flowFunc[model.SimInput, model.SimOutput]
	judgeFlow    flowFunc[assert.JudgeRequest, assert.JudgeVerdict]
	ttsFlow      flowFunc[synthInput, synthOutput]
	sttFlow      flowFunc[sttInput, sttOutput]
	goalFlow     flowFunc[goalInput, goalOutput]
	expectFlow   flowFunc[expectInput, expectOutput]
	generateFlow flowFunc[generateInput, model.Draft]
	tools        []ai.ToolRef
}

type flowFunc[In, Out any] interface {
	Run(ctx context.Context, in In) (Out, error)
}

var _ model.Provider = (*Client)(nil)

// New initialises Genkit from the config (plugins from resolved credentials,
// embedded prompts with overrides) and defines tamesu's flows and evaluator.
func New(ctx context.Context, cfg Config, opts ...Option) (*Client, error) {
	c := &Client{cfg: cfg, logger: cfg.Logger}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	for _, o := range opts {
		o(c)
	}
	if cfg.Models.Default == "" {
		cfg.Models.Default = "googleai/gemini-2.5-flash"
		c.cfg.Models.Default = cfg.Models.Default
	}
	prompts, err := loadPrompts(cfg.PromptOverrideDir)
	if err != nil {
		return nil, err
	}
	c.prompts = prompts
	c.pricing = loadPricing()

	if c.g == nil {
		if cfg.DevUI {
			_ = os.Setenv("GENKIT_ENV", "dev")
		}
		plugins, err := c.plugins(cfg)
		if err != nil {
			return nil, err
		}
		g, err := initGenkit(ctx, plugins, prompts)
		if err != nil {
			return nil, err
		}
		c.g = g
		if err := c.afterInit(cfg); err != nil {
			return nil, err
		}
	} else {
		genkit.LoadPromptDirFromFS(c.g, prompts.fs, promptDir, "")
	}
	c.defineFlows()
	return c, nil
}

// initGenkit wraps genkit.Init, which panics on a plugin failure.
func initGenkit(ctx context.Context, plugins []api.Plugin, prompts *promptSet) (g *genkit.Genkit, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("llm: genkit init: %v", r)
		}
	}()
	g = genkit.Init(ctx, genkit.WithPlugins(plugins...), genkit.WithPromptFS(prompts.fs), genkit.WithPromptDir(promptDir))
	return g, nil
}

// Genkit returns the underlying instance, for callers that want to define
// more flows or tools on it.
func (c *Client) Genkit() *genkit.Genkit { return c.g }

// Usage returns what the client has consumed so far.
func (c *Client) Usage() model.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

// Close releases the client. Genkit has no teardown; traces are flushed by
// the telemetry exporter on process exit.
func (c *Client) Close(context.Context) error { return nil }

// record adds a response's usage and price, and reports the budget.
func (c *Client) record(modelName string, resp *ai.ModelResponse) error {
	if resp == nil {
		return nil
	}
	in, out, chars, seconds := 0, 0, 0, 0.0
	if u := resp.Usage; u != nil {
		in, out = u.InputTokens, u.OutputTokens
		// Genkit fills InputCharacters for every call, so characters are
		// billed only to models that answered with audio (TTS) and audio
		// seconds only to models that heard some (STT).
		if u.OutputAudioFiles > 0 {
			chars = u.InputCharacters
		}
		if u.InputAudioFiles > 0 {
			seconds = u.Custom["audio_seconds"]
		}
	}
	cost := c.pricing.costOf(modelName, in, out, chars, seconds)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage.Calls++
	c.usage.InputTokens += in
	c.usage.OutputTokens += out
	c.usage.CostUSD += cost
	if (c.cfg.Budget.MaxCostUSD > 0 && c.usage.CostUSD > c.cfg.Budget.MaxCostUSD) || (c.cfg.Budget.MaxTokens > 0 && c.usage.Total() > c.cfg.Budget.MaxTokens) {
		return ErrBudgetExceeded
	}
	return nil
}

// overBudget reports whether the budget is already spent, before a call.
func (c *Client) overBudget() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return (c.cfg.Budget.MaxCostUSD > 0 && c.usage.CostUSD > c.cfg.Budget.MaxCostUSD) || (c.cfg.Budget.MaxTokens > 0 && c.usage.Total() > c.cfg.Budget.MaxTokens)
}

// modelFor picks the model for a job, falling back to the default.
func (c *Client) modelFor(job string, override string) string {
	if override != "" {
		return override
	}
	var m string
	switch job {
	case "simulator":
		m = c.cfg.Models.Simulator
	case "judge":
		m = c.cfg.Models.Judge
	case "browser_agent":
		m = c.cfg.Models.BrowserAgent
	case "scenario_generator":
		m = c.cfg.Models.ScenarioGenerator
	}
	if m == "" {
		m = c.cfg.Models.Default
	}
	return m
}

// Judge is assert.Judge, re-exported so Go tests can write llm.Judge("...").
func Judge(rubric string, opts ...assert.JudgeOption) assert.Assertion {
	return assert.Judge(rubric, opts...)
}

// providerOf returns the plugin name of a model reference ("googleai/x" → googleai).
func providerOf(modelRef string) string {
	p, _, _ := strings.Cut(modelRef, "/")
	return p
}
