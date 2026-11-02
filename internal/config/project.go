package config

import "time"

// Project is the project-level configuration in tamesu.yaml (design.md §4).
// Suites can override Defaults, Models and Budgets.
type Project struct {
	Version   int               `yaml:"version" json:"version" jsonschema:"required,enum=1,description=Config format version. Always 1."`
	Suites    []string          `yaml:"suites,omitempty" json:"suites,omitempty" jsonschema:"description=Globs of suite files that run and validate and list use when no paths are given."`
	Genkit    Genkit            `yaml:"genkit,omitempty" json:"genkit,omitempty"`
	Models    Models            `yaml:"models,omitempty" json:"models,omitempty"`
	Speech    Speech            `yaml:"speech,omitempty" json:"speech,omitempty"`
	Cache     CacheConfig       `yaml:"cache,omitempty" json:"cache,omitempty"`
	SIP       SIPConfig         `yaml:"sip,omitempty" json:"sip,omitempty"`
	Defaults  Defaults          `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Budgets   Budgets           `yaml:"budgets,omitempty" json:"budgets,omitempty"`
	LiveKit   LiveKitConfig     `yaml:"livekit,omitempty" json:"livekit,omitempty"`
	Baseline  BaselineConfig    `yaml:"baseline,omitempty" json:"baseline,omitempty"`
	Hooks     *Hooks            `yaml:"hooks,omitempty" json:"hooks,omitempty" jsonschema:"description=Setup and teardown for every suite; a suite's hooks run after the project's."`
	Reporters []string          `yaml:"reporters,omitempty" json:"reporters,omitempty" jsonschema:"description=Reporters to run: console / junit / json / html / markdown / github / slack."`
	Slack     SlackConfig       `yaml:"slack,omitempty" json:"slack,omitempty"`
	Secrets   map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty" jsonschema:"description=Secret name to mamori ref (env:X or aws-sm://... or a precedence chain)."`
	Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty" jsonschema:"description=Key-value labels written into every run's summary such as env: staging; tamesu run --label adds or overrides them. Values take ${VAR}."`
	Vars      map[string]string `yaml:"vars,omitempty" json:"vars,omitempty" jsonschema:"description=Project-wide values for {{vars.x}} and ${VAR} expansion in secret refs."`
}

// Genkit configures the Genkit instance of a run.
type Genkit struct {
	Plugins []string `yaml:"plugins,omitempty" json:"plugins,omitempty" jsonschema:"description=Genkit plugins to initialise: googlegenai anthropic openai ollama deepgram elevenlabs."`
	DevUI   bool     `yaml:"dev_ui,omitempty" json:"dev_ui,omitempty" jsonschema:"description=Export traces to the Genkit Developer UI."`
}

// Models names the Genkit model used for each job. Every value is a Genkit
// model reference such as googleai/gemini-2.5-flash.
type Models struct {
	Default           string            `yaml:"default,omitempty" json:"default,omitempty"`
	Judge             string            `yaml:"judge,omitempty" json:"judge,omitempty"`
	BrowserAgent      string            `yaml:"browser_agent,omitempty" json:"browser_agent,omitempty"`
	Simulator         string            `yaml:"simulator,omitempty" json:"simulator,omitempty"`
	ScenarioGenerator string            `yaml:"scenario_generator,omitempty" json:"scenario_generator,omitempty"`
	Credentials       map[string]string `yaml:"credentials,omitempty" json:"credentials,omitempty" jsonschema:"description=Plugin name to {{secrets.NAME}} when the secret is not named conventionally."`
}

// CacheConfig locates the action cache of browser goals.
type CacheConfig struct {
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty" jsonschema:"description=Directory of the caches (browser action plans and llm answers); default .tamesu/cache."`
	LLM *bool  `yaml:"llm,omitempty" json:"llm,omitempty" jsonschema:"description=Cache the simulator's turns and the judge's verdicts by request (default true); false calls the models every time."`
}

// SlackConfig is the slack reporter's webhook.
type SlackConfig struct {
	Webhook      string `yaml:"webhook,omitempty" json:"webhook,omitempty" jsonschema:"description=The incoming webhook URL as {{secrets.NAME}}."`
	OnlyFailures bool   `yaml:"only_failures,omitempty" json:"only_failures,omitempty" jsonschema:"description=Post only when a test failed or errored or the run regressed against the baseline."`
	Link         string `yaml:"link,omitempty" json:"link,omitempty" jsonschema:"description=A link put at the end of the message such as the job URL; templates and ${VAR} apply."`
}

// BaselineConfig is where the baseline run lives and what counts as a
// regression against it (tamesu run --baseline, tamesu compare).
type BaselineConfig struct {
	Dir                   string   `yaml:"dir,omitempty" json:"dir,omitempty" jsonschema:"description=Where tamesu baseline save keeps the baseline summary; default .tamesu/baseline."`
	MaxLatencyIncreasePct *float64 `yaml:"max_latency_increase_pct,omitempty" json:"max_latency_increase_pct,omitempty" jsonschema:"description=A test's p95 latency metrics may grow this much over the baseline (median over repeats); default 20; 0 disables."`
	MaxCostIncreasePct    *float64 `yaml:"max_cost_increase_pct,omitempty" json:"max_cost_increase_pct,omitempty" jsonschema:"description=A test's llm_cost_usd may grow this much; default 50; 0 disables."`
	FailOnNewFailures     *bool    `yaml:"fail_on_new_failures,omitempty" json:"fail_on_new_failures,omitempty" jsonschema:"description=A test that passed in the baseline and fails now is a regression; default true."`
}

// DefaultBaselineDir is where the baseline summary lives.
const DefaultBaselineDir = ".tamesu/baseline"

// EffectiveDir is the baseline directory with the default applied.
func (b BaselineConfig) EffectiveDir() string {
	if b.Dir == "" {
		return DefaultBaselineDir
	}
	return b.Dir
}

// LLMCacheOn reports whether the llm cache is enabled (the default).
func (c CacheConfig) LLMCacheOn() bool { return c.LLM == nil || *c.LLM }

// Speech configures speech-to-text, text-to-speech and voice activity
// detection for audio mode and sip.
type Speech struct {
	STT SpeechModel `yaml:"stt,omitempty" json:"stt,omitempty"`
	TTS SpeechModel `yaml:"tts,omitempty" json:"tts,omitempty"`
	VAD VADConfig   `yaml:"vad,omitempty" json:"vad,omitempty"`
}

// SpeechModel is a Genkit speech model reference with its options.
type SpeechModel struct {
	Model    string `yaml:"model,omitempty" json:"model,omitempty" jsonschema:"description=Genkit model reference such as deepgram/nova-3 or googleai/gemini-2.5-flash-preview-tts."`
	Language string `yaml:"language,omitempty" json:"language,omitempty"`
	Voice    string `yaml:"voice,omitempty" json:"voice,omitempty"`
}

// VADConfig selects the voice activity detector.
type VADConfig struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty" jsonschema:"enum=energy,enum=silero,description=energy (RMS with a noise floor; the default) or silero (neural; needs a build with onnxruntime)."`
}

// SIPConfig configures phone calls through a LiveKit SIP service.
type SIPConfig struct {
	LiveKitURL         string    `yaml:"livekit_url,omitempty" json:"livekit_url,omitempty" jsonschema:"description=LiveKit server with the SIP service (ws:// or wss://)."`
	OutboundTrunkID    string    `yaml:"outbound_trunk_id,omitempty" json:"outbound_trunk_id,omitempty" jsonschema:"description=The LiveKit outbound trunk tamesu dials through (tamesu sip setup prints it)."`
	InboundTrunkID     string    `yaml:"inbound_trunk_id,omitempty" json:"inbound_trunk_id,omitempty" jsonschema:"description=The LiveKit inbound trunk for calls to inbound_number (informational; tamesu sip setup prints it)."`
	DispatchRuleID     string    `yaml:"dispatch_rule_id,omitempty" json:"dispatch_rule_id,omitempty" jsonschema:"description=The dispatch rule that routes inbound calls into inbound_room (informational)."`
	InboundNumber      string    `yaml:"inbound_number,omitempty" json:"inbound_number,omitempty" jsonschema:"description=The number your trunk routes to tamesu, for inbound tests ({{sip.inbound_number}})."`
	InboundRoom        string    `yaml:"inbound_room,omitempty" json:"inbound_room,omitempty" jsonschema:"description=The room the dispatch rule routes inbound calls into; default tamesu-sip-inbound."`
	MaxConcurrentCalls int       `yaml:"max_concurrent_calls,omitempty" json:"max_concurrent_calls,omitempty"`
	AllowedNumbers     []string  `yaml:"allowed_numbers,omitempty" json:"allowed_numbers,omitempty" jsonschema:"description=E.164 patterns tamesu may dial such as +1800555*; required when a target's to is a template."`
	Trunk              *SIPTrunk `yaml:"trunk,omitempty" json:"trunk,omitempty"`
}

// SIPTrunk is the trunk you bring, for `tamesu sip setup`.
type SIPTrunk struct {
	Address          string   `yaml:"address" json:"address" jsonschema:"required,description=The provider's SIP host such as sip.twilio.com or pbx.example.com:5060."`
	Numbers          []string `yaml:"numbers,omitempty" json:"numbers,omitempty" jsonschema:"description=The trunk's numbers in E.164: caller IDs for outbound calls and the numbers of the inbound trunk."`
	InboundNumbers   []string `yaml:"inbound_numbers,omitempty" json:"inbound_numbers,omitempty" jsonschema:"description=Overrides numbers for the inbound trunk."`
	AuthUsername     string   `yaml:"auth_username,omitempty" json:"auth_username,omitempty" jsonschema:"description=Outbound authentication; use {{secrets.NAME}}."`
	AuthPassword     string   `yaml:"auth_password,omitempty" json:"auth_password,omitempty" jsonschema:"description=Outbound authentication; use {{secrets.NAME}}."`
	Transport        string   `yaml:"transport,omitempty" json:"transport,omitempty" jsonschema:"enum=auto,enum=udp,enum=tcp,enum=tls"`
	AllowedAddresses []string `yaml:"allowed_addresses,omitempty" json:"allowed_addresses,omitempty" jsonschema:"description=IPs, CIDRs or hostnames the inbound trunk accepts calls from."`
}

// Defaults apply to every test unless the suite or the test overrides them.
type Defaults struct {
	Timeout       Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Repeat        int      `yaml:"repeat,omitempty" json:"repeat,omitempty" jsonschema:"description=Times each test runs."`
	Retries       int      `yaml:"retries,omitempty" json:"retries,omitempty"`
	PassThreshold float64  `yaml:"pass_threshold,omitempty" json:"pass_threshold,omitempty" jsonschema:"description=Fraction of repeats that must pass (0 to 1]."`
	MaxGoalSteps  int      `yaml:"max_goal_steps,omitempty" json:"max_goal_steps,omitempty" jsonschema:"description=Browser: the most actions the agent may take per goal step; default 15."`
	// Assert runs on every test, before the test's own assertions; results
	// are named defaults/<assertion>. A suite's entries follow the project's.
	Assert []Assertion `yaml:"assert,omitempty" json:"assert,omitempty" jsonschema:"description=Assertions every test must pass (results named defaults/<name>); a suite's entries run after the project's. Packs apply."`
	// SLO caps metrics on every test: metric name to maximum, checked as
	// slo/<metric>; a metric the run did not measure passes.
	SLO map[string]float64 `yaml:"slo,omitempty" json:"slo,omitempty" jsonschema:"description=Maximum per metric on every test such as turn_latency_ms_p95: 3000 or llm_cost_usd: 0.05 (results named slo/<metric>); a metric a test does not measure passes."`
}

// Budgets cap what a run may spend on tamesu's own model calls.
type Budgets struct {
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`
	MaxTokens  int     `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
}

// LiveKitConfig limits the LiveKit target.
type LiveKitConfig struct {
	MaxConcurrentRooms int `yaml:"max_concurrent_rooms,omitempty" json:"max_concurrent_rooms,omitempty"`
}

// DefaultProject is the configuration used when there is no tamesu.yaml, and
// the base a tamesu.yaml is decoded over.
func DefaultProject() Project {
	return Project{
		Version:   1,
		Suites:    []string{"tests/**/*.yaml"},
		Models:    Models{Default: "googleai/gemini-2.5-flash"},
		SIP:       SIPConfig{MaxConcurrentCalls: 2},
		Defaults:  Defaults{Timeout: Duration(3 * time.Minute), Repeat: 1, Retries: 0, PassThreshold: 1.0},
		Budgets:   Budgets{MaxCostUSD: 5, MaxTokens: 2_000_000},
		LiveKit:   LiveKitConfig{MaxConcurrentRooms: 4},
		Reporters: []string{"console", "junit", "html"},
	}
}

// Merge returns d with every non-zero field of o applied on top.
func (d Defaults) Merge(o *Defaults) Defaults {
	if o == nil {
		return d
	}
	if o.Timeout != 0 {
		d.Timeout = o.Timeout
	}
	if o.Repeat != 0 {
		d.Repeat = o.Repeat
	}
	if o.Retries != 0 {
		d.Retries = o.Retries
	}
	if o.PassThreshold != 0 {
		d.PassThreshold = o.PassThreshold
	}
	if o.MaxGoalSteps != 0 {
		d.MaxGoalSteps = o.MaxGoalSteps
	}
	if len(o.Assert) > 0 {
		d.Assert = append(append([]Assertion(nil), d.Assert...), o.Assert...)
	}
	if len(o.SLO) > 0 {
		slo := make(map[string]float64, len(d.SLO)+len(o.SLO))
		for k, v := range d.SLO {
			slo[k] = v
		}
		for k, v := range o.SLO {
			slo[k] = v
		}
		d.SLO = slo
	}
	return d
}

// Merge returns m with every non-empty field of o applied on top.
func (m Models) Merge(o *Models) Models {
	if o == nil {
		return m
	}
	if o.Default != "" {
		m.Default = o.Default
	}
	if o.Judge != "" {
		m.Judge = o.Judge
	}
	if o.BrowserAgent != "" {
		m.BrowserAgent = o.BrowserAgent
	}
	if o.Simulator != "" {
		m.Simulator = o.Simulator
	}
	if o.ScenarioGenerator != "" {
		m.ScenarioGenerator = o.ScenarioGenerator
	}
	if len(o.Credentials) > 0 {
		creds := make(map[string]string, len(m.Credentials)+len(o.Credentials))
		for k, v := range m.Credentials {
			creds[k] = v
		}
		for k, v := range o.Credentials {
			creds[k] = v
		}
		m.Credentials = creds
	}
	return m
}

// Merge returns b with every non-zero field of o applied on top.
func (b Budgets) Merge(o *Budgets) Budgets {
	if o == nil {
		return b
	}
	if o.MaxCostUSD != 0 {
		b.MaxCostUSD = o.MaxCostUSD
	}
	if o.MaxTokens != 0 {
		b.MaxTokens = o.MaxTokens
	}
	return b
}
