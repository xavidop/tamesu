package config

// Target types (design.md §2).
const (
	TargetBrowser = "browser"
	TargetREST    = "rest"
	TargetLiveKit = "livekit"
	TargetSIP     = "sip"
)

// TargetTypes lists the target types a suite may declare, in documentation order.
func TargetTypes() []string { return []string{TargetBrowser, TargetREST, TargetLiveKit, TargetSIP} }

// IsConversational reports whether a target type talks in turns (everything
// but the browser).
func IsConversational(targetType string) bool { return targetType != TargetBrowser && targetType != "" }

// ActionNames are the deterministic browser actions a step may take.
var ActionNames = []string{"navigate", "click", "fill", "press", "wait", "screenshot", "eval_js", "assert_url", "assert_text"}

// AssertionKinds lists every assertion key, in documentation order.
var AssertionKinds = []string{
	"judge", "contains", "not_contains", "regex", "json_path", "status",
	"tool_called", "tool_not_called", "dtmf_received", "call_ended_by", "transferred_to",
	"evaluator", "metric", "turns", "custom",
}

// Suite is one YAML suite file (design.md §5): targets, defaults and tests.
type Suite struct {
	Suite    string            `yaml:"suite" json:"suite" jsonschema:"required,description=Suite name."`
	Tags     []string          `yaml:"tags,omitempty" json:"tags,omitempty" jsonschema:"description=Tags every test of the suite carries."`
	Vars     map[string]string `yaml:"vars,omitempty" json:"vars,omitempty" jsonschema:"description=Values for {{vars.x}} and for ${VAR} expansion in secret refs."`
	Secrets  map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty" jsonschema:"description=Secret name to mamori ref; overrides the project entry of the same name."`
	Defaults *Defaults         `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Models   *Models           `yaml:"models,omitempty" json:"models,omitempty"`
	Budgets  *Budgets          `yaml:"budgets,omitempty" json:"budgets,omitempty"`
	Targets  map[string]Target `yaml:"targets" json:"targets" jsonschema:"required,description=The systems under test by name."`
	Tests    []Test            `yaml:"tests" json:"tests" jsonschema:"required"`

	// Path is the file the suite was loaded from. It is not part of the format.
	Path string `yaml:"-" json:"-"`
}

// Target is a system under test. Type selects which of the other fields apply.
type Target struct {
	Type string `yaml:"type" json:"type" jsonschema:"required,enum=browser,enum=rest,enum=livekit,enum=sip"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty" jsonschema:"description=The page (browser) or endpoint (rest) or LiveKit server (livekit)."`

	// browser
	Browser        *BrowserOptions `yaml:"browser,omitempty" json:"browser,omitempty"`
	Auth           *BrowserAuth    `yaml:"auth,omitempty" json:"auth,omitempty"`
	AllowedDomains []string        `yaml:"allowed_domains,omitempty" json:"allowed_domains,omitempty" jsonschema:"description=Domains the browser agent may navigate to; defaults to the url's domain."`

	// rest
	Method    string            `yaml:"method,omitempty" json:"method,omitempty" jsonschema:"description=HTTP method; defaults to POST."`
	Headers   map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Request   *RESTRequest      `yaml:"request,omitempty" json:"request,omitempty"`
	Response  *RESTResponse     `yaml:"response,omitempty" json:"response,omitempty"`
	Streaming string            `yaml:"streaming,omitempty" json:"streaming,omitempty" jsonschema:"enum=none,enum=sse,enum=websocket"`
	Session   *RESTSession      `yaml:"session,omitempty" json:"session,omitempty"`

	// livekit
	APIKey              string    `yaml:"api_key,omitempty" json:"api_key,omitempty" jsonschema:"description=Use {{secrets.NAME}}."`
	APISecret           string    `yaml:"api_secret,omitempty" json:"api_secret,omitempty" jsonschema:"description=Use {{secrets.NAME}}."`
	Room                string    `yaml:"room,omitempty" json:"room,omitempty" jsonschema:"description=Room name template; defaults to tamesu-{{uuid}}."`
	Dispatch            *Dispatch `yaml:"dispatch,omitempty" json:"dispatch,omitempty"`
	AgentIdentityPrefix string    `yaml:"agent_identity_prefix,omitempty" json:"agent_identity_prefix,omitempty" jsonschema:"description=How to recognise the agent participant; defaults to kind=agent."`
	Mode                string    `yaml:"mode,omitempty" json:"mode,omitempty" jsonschema:"enum=text,enum=audio"`
	DataTopic           string    `yaml:"data_topic,omitempty" json:"data_topic,omitempty" jsonschema:"description=Text-mode transport topic; defaults to lk.chat."`
	JoinTimeout         Duration  `yaml:"join_timeout,omitempty" json:"join_timeout,omitempty"`
	TranscriptSource    string    `yaml:"transcript_source,omitempty" json:"transcript_source,omitempty" jsonschema:"enum=auto,enum=agent,enum=stt,description=Audio mode: where the agent's words come from; auto (default) takes the agent's own transcription when it publishes one and transcribes its audio otherwise."`

	// sip
	Direction       string   `yaml:"direction,omitempty" json:"direction,omitempty" jsonschema:"enum=outbound,enum=inbound"`
	To              string   `yaml:"to,omitempty" json:"to,omitempty" jsonschema:"description=The agent's phone number in E.164 (outbound)."`
	From            string   `yaml:"from,omitempty" json:"from,omitempty" jsonschema:"description=Caller ID presented by the trunk."`
	TrunkID         string   `yaml:"trunk_id,omitempty" json:"trunk_id,omitempty" jsonschema:"description=LiveKit SIP trunk; defaults to sip.outbound_trunk_id."`
	RingTimeout     Duration `yaml:"ring_timeout,omitempty" json:"ring_timeout,omitempty"`
	MaxCallDuration Duration `yaml:"max_call_duration,omitempty" json:"max_call_duration,omitempty"`
	Trigger         *Trigger `yaml:"trigger,omitempty" json:"trigger,omitempty"`
}

// BrowserOptions configures the browser.
type BrowserOptions struct {
	Engine   string `yaml:"engine,omitempty" json:"engine,omitempty" jsonschema:"enum=chromium"`
	Viewport string `yaml:"viewport,omitempty" json:"viewport,omitempty" jsonschema:"description=WIDTHxHEIGHT such as 1280x800."`
	Locale   string `yaml:"locale,omitempty" json:"locale,omitempty"`
}

// BrowserAuth pre-authenticates the browser.
type BrowserAuth struct {
	StorageState string `yaml:"storage_state,omitempty" json:"storage_state,omitempty" jsonschema:"description=Path to a saved storage state (cookies and local storage)."`
}

// RESTRequest templates how a user turn is sent.
type RESTRequest struct {
	Body any `yaml:"body,omitempty" json:"body,omitempty" jsonschema:"description=Request body template; {{message}} is the user turn and {{session}} the session id."`
	Open any `yaml:"open,omitempty" json:"open,omitempty" jsonschema:"description=streaming websocket: a message template sent once after connecting."`
}

// RESTResponse says how the agent turn is read from the response.
type RESTResponse struct {
	Text      string   `yaml:"text,omitempty" json:"text,omitempty" jsonschema:"description=JSONPath of the agent's reply such as $.reply."`
	ToolCalls string   `yaml:"tool_calls,omitempty" json:"tool_calls,omitempty" jsonschema:"description=JSONPath of the tool call names."`
	Done      string   `yaml:"done,omitempty" json:"done,omitempty" jsonschema:"description=JSONPath of a boolean that ends the session."`
	TurnDone  string   `yaml:"turn_done,omitempty" json:"turn_done,omitempty" jsonschema:"description=streaming websocket: JSONPath of a boolean that ends the turn; otherwise the turn ends after idle."`
	Idle      Duration `yaml:"idle,omitempty" json:"idle,omitempty" jsonschema:"description=streaming websocket: silence that ends a turn when no message marks it; default 2s."`
}

// RESTSession configures the session id.
type RESTSession struct {
	ID string `yaml:"id,omitempty" json:"id,omitempty" jsonschema:"description=Session id template; defaults to {{uuid}}."`
}

// Dispatch requests an explicit LiveKit agent dispatch.
type Dispatch struct {
	AgentName string `yaml:"agent_name" json:"agent_name" jsonschema:"required"`
}

// Trigger makes an agent call tamesu (inbound sip).
type Trigger struct {
	HTTP    *HTTPTrigger `yaml:"http,omitempty" json:"http,omitempty"`
	Command string       `yaml:"command,omitempty" json:"command,omitempty" jsonschema:"description=A shell command that makes the agent dial sip.inbound_number."`
}

// HTTPTrigger is an HTTP request that makes the agent dial.
type HTTPTrigger struct {
	URL     string            `yaml:"url" json:"url" jsonschema:"required"`
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	Body    any               `yaml:"body,omitempty" json:"body,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// Test is one scenario run against one target.
type Test struct {
	Name          string      `yaml:"name" json:"name" jsonschema:"required"`
	Target        string      `yaml:"target" json:"target" jsonschema:"required,description=A key of targets."`
	Tags          []string    `yaml:"tags,omitempty" json:"tags,omitempty"`
	Mode          string      `yaml:"mode,omitempty" json:"mode,omitempty" jsonschema:"enum=text,enum=audio,description=Voice tests: overrides the target's mode."`
	Repeat        *int        `yaml:"repeat,omitempty" json:"repeat,omitempty" jsonschema:"description=Times the test runs to measure variance."`
	PassThreshold *float64    `yaml:"pass_threshold,omitempty" json:"pass_threshold,omitempty"`
	Retries       *int        `yaml:"retries,omitempty" json:"retries,omitempty"`
	Timeout       *Duration   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Seed          *int64      `yaml:"seed,omitempty" json:"seed,omitempty" jsonschema:"description=Seed for the simulator prompts and the behaviour RNG."`
	Steps         []Step      `yaml:"steps,omitempty" json:"steps,omitempty" jsonschema:"description=Browser tests only."`
	Persona       *Persona    `yaml:"persona,omitempty" json:"persona,omitempty"`
	Scenario      *Scenario   `yaml:"scenario,omitempty" json:"scenario,omitempty"`
	Assert        []Assertion `yaml:"assert,omitempty" json:"assert,omitempty"`
}

// Step is one browser step: a natural-language goal, an agentic expectation
// or a deterministic action.
type Step struct {
	Goal   string         `yaml:"goal,omitempty" json:"goal,omitempty" jsonschema:"description=Agentic: the browser agent acts until the goal is met."`
	Expect string         `yaml:"expect,omitempty" json:"expect,omitempty" jsonschema:"description=Agentic assertion against the current page."`
	Action map[string]any `yaml:"action,omitempty" json:"action,omitempty" jsonschema:"description=Deterministic: one of navigate click fill press wait screenshot eval_js assert_url assert_text."`
}

// Kind returns "goal", "expect", "action" when exactly one is set, else "".
func (s Step) Kind() string {
	k := s.Kinds()
	if len(k) == 1 {
		return k[0]
	}
	return ""
}

// Kinds lists which of goal, expect and action are set.
func (s Step) Kinds() []string {
	var k []string
	if s.Goal != "" {
		k = append(k, "goal")
	}
	if s.Expect != "" {
		k = append(k, "expect")
	}
	if s.Action != nil {
		k = append(k, "action")
	}
	return k
}

// Persona is the simulated user of an agent test.
type Persona struct {
	Name      string     `yaml:"name,omitempty" json:"name,omitempty"`
	Style     string     `yaml:"style,omitempty" json:"style,omitempty" jsonschema:"description=How the persona talks such as polite or impatient."`
	Language  string     `yaml:"language,omitempty" json:"language,omitempty" jsonschema:"description=BCP 47 language such as es or en-GB."`
	Voice     string     `yaml:"voice,omitempty" json:"voice,omitempty" jsonschema:"description=TTS voice such as en-GB-female."`
	Behaviors *Behaviors `yaml:"behaviors,omitempty" json:"behaviors,omitempty"`
}

// Behaviors are the persona's audio-mode habits.
type Behaviors struct {
	InterruptProbability float64  `yaml:"interrupt_probability,omitempty" json:"interrupt_probability,omitempty" jsonschema:"minimum=0,maximum=1"`
	SilenceAfterTurnMs   []int    `yaml:"silence_after_turn_ms,omitempty" json:"silence_after_turn_ms,omitempty" jsonschema:"description=[min max] milliseconds of silence after each turn."`
	BackgroundNoise      string   `yaml:"background_noise,omitempty" json:"background_noise,omitempty" jsonschema:"description=A noise preset (cafe street office white) or a WAV file mixed under the caller's voice."`
	BackgroundNoiseSNRdB *float64 `yaml:"background_noise_snr_db,omitempty" json:"background_noise_snr_db,omitempty" jsonschema:"description=Signal-to-noise ratio of the background noise in dB; default 20."`
	LineQuality          string   `yaml:"line_quality,omitempty" json:"line_quality,omitempty" jsonschema:"enum=pstn,enum=mobile_poor,enum=voip_good"`
}

// NoisePresets are the background_noise presets the audio module synthesises
// (kept in step with audio/noise.Presets; anything else must be a .wav path).
var NoisePresets = []string{"cafe", "office", "street", "white"}

// Scenario is what the persona tries to achieve and when to stop.
type Scenario struct {
	Goal     string       `yaml:"goal" json:"goal" jsonschema:"required"`
	MaxTurns int          `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
	StopWhen string       `yaml:"stop_when,omitempty" json:"stop_when,omitempty" jsonschema:"description=A plain-language stop condition judged after each agent turn."`
	Script   []ScriptStep `yaml:"script,omitempty" json:"script,omitempty" jsonschema:"description=Deterministic steps mixed with free conversation (sip and livekit audio)."`
}

// ScriptStep is a deterministic step of a voice scenario.
type ScriptStep struct {
	WaitFor string `yaml:"wait_for,omitempty" json:"wait_for,omitempty" jsonschema:"description=Wait until the transcript fuzzily contains this text."`
	DTMF    string `yaml:"dtmf,omitempty" json:"dtmf,omitempty" jsonschema:"description=Digits to send."`
	Say     string `yaml:"say,omitempty" json:"say,omitempty" jsonschema:"description=Exact text to say before the persona takes over."`
}

// Kind returns "wait_for", "dtmf", "say" when exactly one is set, else "".
func (s ScriptStep) Kind() string {
	var k []string
	if s.WaitFor != "" {
		k = append(k, "wait_for")
	}
	if s.DTMF != "" {
		k = append(k, "dtmf")
	}
	if s.Say != "" {
		k = append(k, "say")
	}
	if len(k) == 1 {
		return k[0]
	}
	return ""
}

// Assertion is one check. Exactly one of the kind fields is set.
type Assertion struct {
	Judge         *Judge             `yaml:"judge,omitempty" json:"judge,omitempty"`
	Contains      *string            `yaml:"contains,omitempty" json:"contains,omitempty" jsonschema:"description=The agent transcript contains this text."`
	NotContains   *string            `yaml:"not_contains,omitempty" json:"not_contains,omitempty"`
	Regex         *string            `yaml:"regex,omitempty" json:"regex,omitempty" jsonschema:"description=A regular expression the agent transcript matches."`
	JSONPath      *JSONPathAssertion `yaml:"json_path,omitempty" json:"json_path,omitempty" jsonschema:"description=rest only."`
	Status        *int               `yaml:"status,omitempty" json:"status,omitempty" jsonschema:"description=rest only: the HTTP status of every turn."`
	ToolCalled    *ToolCall          `yaml:"tool_called,omitempty" json:"tool_called,omitempty"`
	ToolNotCalled *ToolCall          `yaml:"tool_not_called,omitempty" json:"tool_not_called,omitempty"`
	DTMFReceived  *DTMFAssertion     `yaml:"dtmf_received,omitempty" json:"dtmf_received,omitempty" jsonschema:"description=sip and livekit audio: digits the agent sent."`
	CallEndedBy   StringList         `yaml:"call_ended_by,omitempty" json:"call_ended_by,omitempty" jsonschema:"description=sip: agent caller timeout error no_answer busy."`
	TransferredTo *Matches           `yaml:"transferred_to,omitempty" json:"transferred_to,omitempty" jsonschema:"description=sip: target of a SIP REFER."`
	Evaluator     *Evaluator         `yaml:"evaluator,omitempty" json:"evaluator,omitempty"`
	Metric        *MetricAssertion   `yaml:"metric,omitempty" json:"metric,omitempty"`
	Turns         *IntCompare        `yaml:"turns,omitempty" json:"turns,omitempty" jsonschema:"description=Number of agent turns."`
	Custom        *string            `yaml:"custom,omitempty" json:"custom,omitempty" jsonschema:"description=A Go-registered assertion by name."`
	OnTurn        OnTurn             `yaml:"on_turn,omitempty" json:"on_turn,omitempty"`
}

// Kinds lists the assertion keys that are set, in AssertionKinds order.
func (a Assertion) Kinds() []string {
	set := map[string]bool{
		"judge":           a.Judge != nil,
		"contains":        a.Contains != nil,
		"not_contains":    a.NotContains != nil,
		"regex":           a.Regex != nil,
		"json_path":       a.JSONPath != nil,
		"status":          a.Status != nil,
		"tool_called":     a.ToolCalled != nil,
		"tool_not_called": a.ToolNotCalled != nil,
		"dtmf_received":   a.DTMFReceived != nil,
		"call_ended_by":   len(a.CallEndedBy) > 0,
		"transferred_to":  a.TransferredTo != nil,
		"evaluator":       a.Evaluator != nil,
		"metric":          a.Metric != nil,
		"turns":           a.Turns != nil,
		"custom":          a.Custom != nil,
	}
	var k []string
	for _, name := range AssertionKinds {
		if set[name] {
			k = append(k, name)
		}
	}
	return k
}

// Kind returns the single assertion key that is set, or "" when there is not
// exactly one.
func (a Assertion) Kind() string {
	k := a.Kinds()
	if len(k) == 1 {
		return k[0]
	}
	return ""
}
