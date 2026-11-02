package config

import "time"

// Target types (design.md §2).
const (
	TargetBrowser  = "browser"
	TargetREST     = "rest"
	TargetLiveKit  = "livekit"
	TargetSIP      = "sip"
	TargetRealtime = "realtime"
	TargetGRPC     = "grpc"
	TargetPipecat  = "pipecat"
)

// TargetTypes lists the target types a suite may declare, in documentation order.
func TargetTypes() []string {
	return []string{TargetBrowser, TargetREST, TargetLiveKit, TargetSIP, TargetRealtime, TargetGRPC, TargetPipecat}
}

// IsConversational reports whether a target type talks in turns (everything
// but the browser).
func IsConversational(targetType string) bool { return targetType != TargetBrowser && targetType != "" }

// ActionNames are the deterministic browser actions a step may take.
var ActionNames = []string{"navigate", "click", "fill", "press", "wait", "screenshot", "eval_js", "assert_url", "assert_text", "mock", "a11y", "visual"}

// AssertionKinds lists every assertion key, in documentation order.
var AssertionKinds = []string{
	"judge", "contains", "not_contains", "regex", "not_regex", "json_path", "status",
	"tool_called", "tool_not_called", "tool_sequence", "dtmf_received", "call_ended_by", "transferred_to",
	"evaluator", "metric", "turns", "captured", "golden", "custom", "pack",
}

// Suite is one YAML suite file (design.md §5): targets, defaults and tests.
type Suite struct {
	Suite    string                 `yaml:"suite" json:"suite" jsonschema:"required,description=Suite name."`
	Tags     []string               `yaml:"tags,omitempty" json:"tags,omitempty" jsonschema:"description=Tags every test of the suite carries."`
	Vars     map[string]string      `yaml:"vars,omitempty" json:"vars,omitempty" jsonschema:"description=Values for {{vars.x}} and for ${VAR} expansion in secret refs."`
	Secrets  map[string]string      `yaml:"secrets,omitempty" json:"secrets,omitempty" jsonschema:"description=Secret name to mamori ref; overrides the project entry of the same name."`
	Defaults *Defaults              `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Models   *Models                `yaml:"models,omitempty" json:"models,omitempty"`
	Budgets  *Budgets               `yaml:"budgets,omitempty" json:"budgets,omitempty"`
	Targets  map[string]Target      `yaml:"targets" json:"targets" jsonschema:"required,description=The systems under test by name."`
	Hooks    *Hooks                 `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Packs    map[string][]Assertion `yaml:"packs,omitempty" json:"packs,omitempty" jsonschema:"description=Reusable assertion sets by name for assert entries of the form pack: name; a name also used by a built-in pack (prompt_injection / pii_leakage / system_prompt_leakage) replaces it."`
	Tests    []Test                 `yaml:"tests" json:"tests" jsonschema:"required"`

	// Path is the file the suite was loaded from. It is not part of the format.
	Path string `yaml:"-" json:"-"`
}

// Target is a system under test. Type selects which of the other fields apply.
type Target struct {
	Type string `yaml:"type" json:"type" jsonschema:"required,enum=browser,enum=rest,enum=livekit,enum=sip,enum=realtime,enum=grpc,enum=pipecat"`
	URL  string `yaml:"url,omitempty" json:"url,omitempty" jsonschema:"description=The page (browser) or endpoint (rest / realtime / pipecat) or grpc://host:port (grpc) or LiveKit server (livekit)."`

	// browser
	Browser        *BrowserOptions `yaml:"browser,omitempty" json:"browser,omitempty"`
	Auth           *BrowserAuth    `yaml:"auth,omitempty" json:"auth,omitempty"`
	AllowedDomains []string        `yaml:"allowed_domains,omitempty" json:"allowed_domains,omitempty" jsonschema:"description=Domains the browser agent may navigate to; defaults to the url's domain."`

	// rest (headers also realtime)
	Method    string            `yaml:"method,omitempty" json:"method,omitempty" jsonschema:"description=HTTP method; defaults to POST."`
	Headers   map[string]string `yaml:"headers,omitempty" json:"headers,omitempty" jsonschema:"description=Request (rest) or handshake (realtime and pipecat) headers or call metadata (grpc); put credentials here as {{secrets.NAME}}."`
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
	Mode                string    `yaml:"mode,omitempty" json:"mode,omitempty" jsonschema:"enum=text,enum=audio,description=livekit / realtime / pipecat: text (default) or audio."`
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

	// realtime (the OpenAI Realtime protocol over a websocket; url, headers and mode apply too)
	Model         string         `yaml:"model,omitempty" json:"model,omitempty" jsonschema:"description=realtime: added as ?model= to the url (such as gpt-realtime)."`
	SessionUpdate map[string]any `yaml:"session_update,omitempty" json:"session_update,omitempty" jsonschema:"description=realtime: merged into the session.update event (instructions / tools / voice / output modalities)."`
	Protocol      string         `yaml:"protocol,omitempty" json:"protocol,omitempty" jsonschema:"enum=ga,enum=beta,description=realtime: the event dialect; ga (default) or beta (modalities and the OpenAI-Beta header)."`
	DoneTool      string         `yaml:"done_tool,omitempty" json:"done_tool,omitempty" jsonschema:"description=realtime: a function call with this name ends the session; default end_session."`
	TurnTimeout   Duration       `yaml:"turn_timeout,omitempty" json:"turn_timeout,omitempty" jsonschema:"description=realtime / grpc / pipecat: how long to wait for the end of a turn; default 60s."`
	ToolResults   map[string]any `yaml:"tool_results,omitempty" json:"tool_results,omitempty" jsonschema:"description=realtime and pipecat: answers to the agent's function calls by function name. The value (any JSON; string leaves may use {{args.<key>}}) goes back as the function's output and the response continues. Unlisted functions and done_tool are recorded only."`
	AudioFormat   string         `yaml:"audio_format,omitempty" json:"audio_format,omitempty" jsonschema:"enum=pcm16,enum=g711_ulaw,enum=g711_alaw,description=realtime audio mode: the agent's output audio; pcm16 (default) or G.711 at 8 kHz."`

	// pipecat (a Pipecat websocket transport with the protobuf frame serializer; url, headers, mode, turn_timeout and tool_results apply too)
	SampleRate int      `yaml:"sample_rate,omitempty" json:"sample_rate,omitempty" jsonschema:"description=pipecat audio mode: the transport's sample rate in Hz; default 16000."`
	Idle       Duration `yaml:"idle,omitempty" json:"idle,omitempty" jsonschema:"description=pipecat: silence after the agent's last message or audio that ends a turn when no bot-stopped-speaking / bot-llm-stopped arrives; default 2s."`
}

// RealtimeAudioFormats are the values of a realtime target's audio_format.
var RealtimeAudioFormats = []string{"pcm16", "g711_ulaw", "g711_alaw"}

// BrowserRecordingFormats are the values of browser.recording.
var BrowserRecordingFormats = []string{"auto", "gif", "webm", "off"}

// BrowserOptions configures the browser.
type BrowserOptions struct {
	Engine   string `yaml:"engine,omitempty" json:"engine,omitempty" jsonschema:"enum=chromium"`
	Viewport string `yaml:"viewport,omitempty" json:"viewport,omitempty" jsonschema:"description=WIDTHxHEIGHT such as 1280x800."`
	Locale   string `yaml:"locale,omitempty" json:"locale,omitempty"`
	// Mocks answer matching requests instead of the network.
	Mocks []BrowserMock `yaml:"mocks,omitempty" json:"mocks,omitempty" jsonschema:"description=Requests answered by tamesu instead of the network from the start of the session; the mock action adds more at run time."`
	// Recording selects the session recording in the run directory.
	Recording string `yaml:"recording,omitempty" json:"recording,omitempty" jsonschema:"enum=auto,enum=gif,enum=webm,enum=off,description=The session recording written next to the screenshots. auto (the default) writes recording.webm when ffmpeg is installed and recording.gif otherwise; off disables it."`
}

// BrowserMock is one mocked request.
type BrowserMock struct {
	URL     string            `yaml:"url" json:"url" jsonschema:"required,description=A glob on the full URL (** crosses slashes) or a substring."`
	Method  string            `yaml:"method,omitempty" json:"method,omitempty" jsonschema:"description=Only this method; default any."`
	Status  int               `yaml:"status,omitempty" json:"status,omitempty" jsonschema:"description=The answer's status; default 200."`
	Body    any               `yaml:"body,omitempty" json:"body,omitempty" jsonschema:"description=The answer: a string as is or any value as JSON."`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	DelayMs int               `yaml:"delay_ms,omitempty" json:"delay_ms,omitempty" jsonschema:"description=Hold the answer back this long."`
	Abort   bool              `yaml:"abort,omitempty" json:"abort,omitempty" jsonschema:"description=Fail the request instead of answering."`
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

// Hooks are commands or HTTP requests run around a suite and its runs.
type Hooks struct {
	BeforeAll  []Hook `yaml:"before_all,omitempty" json:"before_all,omitempty" jsonschema:"description=Run once before the suite's first test (the project's first)."`
	AfterAll   []Hook `yaml:"after_all,omitempty" json:"after_all,omitempty" jsonschema:"description=Run once after the suite's last test."`
	BeforeEach []Hook `yaml:"before_each,omitempty" json:"before_each,omitempty" jsonschema:"description=Run before every run of every test; a failure is an infrastructure error for that run."`
	AfterEach  []Hook `yaml:"after_each,omitempty" json:"after_each,omitempty" jsonschema:"description=Run after every run; a failure is logged and does not change the run's result."`
}

// Hook is one setup or teardown step: a shell command or an HTTP request.
// Templates render with the suite's vars and secrets; the environment of a
// command carries TAMESU_SUITE / TAMESU_TEST / TAMESU_RUN / TAMESU_RUN_DIR /
// TAMESU_SESSION where they apply.
type Hook struct {
	Command string       `yaml:"command,omitempty" json:"command,omitempty" jsonschema:"description=A shell command (sh -c)."`
	HTTP    *HTTPTrigger `yaml:"http,omitempty" json:"http,omitempty" jsonschema:"description=An HTTP request; a status of 400 or more fails the hook."`
	Timeout Duration     `yaml:"timeout,omitempty" json:"timeout,omitempty" jsonschema:"description=How long the hook may take; default 60s."`
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
	Name          string            `yaml:"name" json:"name" jsonschema:"required"`
	Target        string            `yaml:"target" json:"target" jsonschema:"required,description=A key of targets."`
	Tags          []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Mode          string            `yaml:"mode,omitempty" json:"mode,omitempty" jsonschema:"enum=text,enum=audio,description=Voice tests: overrides the target's mode."`
	Repeat        *int              `yaml:"repeat,omitempty" json:"repeat,omitempty" jsonschema:"description=Times the test runs to measure variance."`
	PassThreshold *float64          `yaml:"pass_threshold,omitempty" json:"pass_threshold,omitempty"`
	Retries       *int              `yaml:"retries,omitempty" json:"retries,omitempty"`
	Timeout       *Duration         `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Seed          *int64            `yaml:"seed,omitempty" json:"seed,omitempty" jsonschema:"description=Seed for the simulator prompts and the behaviour RNG."`
	Vars          map[string]string `yaml:"vars,omitempty" json:"vars,omitempty" jsonschema:"description=Values for {{vars.<key>}} in this test; on top of the suite's vars and under --var."`
	Cases         []Case            `yaml:"cases,omitempty" json:"cases,omitempty" jsonschema:"description=Parameterised: the test runs once per case with the case's vars; each becomes a test named <name> [<case>] tagged case:<name>."`
	CasesFile     string            `yaml:"cases_file,omitempty" json:"cases_file,omitempty" jsonschema:"description=Cases from a file next to the suite: a CSV with a header row (a name column names the case) or a JSON array of objects (a name key names the case)."`
	Steps         []Step            `yaml:"steps,omitempty" json:"steps,omitempty" jsonschema:"description=Browser tests only."`
	Persona       *Persona          `yaml:"persona,omitempty" json:"persona,omitempty"`
	Scenario      *Scenario         `yaml:"scenario,omitempty" json:"scenario,omitempty"`
	// Line is the test's line in its suite file, for annotations. Not part
	// of the format.
	Line int `yaml:"-" json:"-"`

	Capture []Capture   `yaml:"capture,omitempty" json:"capture,omitempty" jsonschema:"description=Values taken from the agent's turns as they arrive (the first match wins) and offered as {{captured.<name>}} to later script say steps / tool_results / hooks / assertions."`
	Assert  []Assertion `yaml:"assert,omitempty" json:"assert,omitempty"`

	Quarantine *Quarantine `yaml:"quarantine,omitempty" json:"quarantine,omitempty" jsonschema:"description=Known flaky: the test still runs and its failure is reported as quarantined but it does not fail the run or count as a regression."`
}

// Quarantine marks a test as known flaky. The test runs and is reported, but
// a failure neither fails the run nor counts as a regression, until the
// quarantine expires.
type Quarantine struct {
	Reason string `yaml:"reason" json:"reason" jsonschema:"required,description=Why the test is quarantined; usually the ticket that tracks it."`
	Until  string `yaml:"until,omitempty" json:"until,omitempty" jsonschema:"description=The last day (YYYY-MM-DD) of the quarantine; from the next day the test counts again. Without it the quarantine lasts until removed."`
}

// QuarantineDateLayout is the format of quarantine.until.
const QuarantineDateLayout = "2006-01-02"

// Active reports whether the quarantine covers now: always when until is
// unset, else through the whole of the until day (UTC).
func (q *Quarantine) Active(now time.Time) bool {
	if q == nil {
		return false
	}
	if q.Until == "" {
		return true
	}
	d, err := time.Parse(QuarantineDateLayout, q.Until)
	if err != nil {
		return false
	}
	return now.UTC().Before(d.Add(24 * time.Hour))
}

// Capture names a value to take from the agent's turns.
type Capture struct {
	Name     string `yaml:"name" json:"name" jsonschema:"required,description=The name; the value is {{captured.<name>}}."`
	Regex    string `yaml:"regex,omitempty" json:"regex,omitempty" jsonschema:"description=A regular expression on the agent's text; the first group (else the whole match) is the value."`
	JSONPath string `yaml:"json_path,omitempty" json:"json_path,omitempty" jsonschema:"description=A JSONPath on the turn's raw response (rest / websocket / realtime events)."`
}

// GoldenAssertion compares the agent's turns with reference turns.
type GoldenAssertion struct {
	File  string   `yaml:"file,omitempty" json:"file,omitempty" jsonschema:"description=A JSON file next to the suite: an array of agent turns or a transcript.json; written by tamesu run --update-golden."`
	Turns []string `yaml:"turns,omitempty" json:"turns,omitempty" jsonschema:"description=Inline reference turns instead of a file."`
	Mode  string   `yaml:"mode,omitempty" json:"mode,omitempty" jsonschema:"enum=exact,enum=similar,enum=judge,description=exact (normalised text) / similar (word overlap at least min; the default) / judge (the judge decides whether each turn means the same)."`
	Min   float64  `yaml:"min,omitempty" json:"min,omitempty" jsonschema:"description=similar: the minimum word overlap (0 to 1); default 0.8."`
}

// GoldenModes are the values of golden.mode.
var GoldenModes = []string{"exact", "similar", "judge"}

// CapturedAssertion is the captured assertion: a captured value exists and
// equals or matches something.
type CapturedAssertion struct {
	Name    string  `yaml:"name" json:"name" jsonschema:"required"`
	Equals  *string `yaml:"equals,omitempty" json:"equals,omitempty"`
	Matches *string `yaml:"matches,omitempty" json:"matches,omitempty" jsonschema:"description=A regular expression the value must match."`
}

// Step is one browser step: a natural-language goal, an agentic expectation
// or a deterministic action.
type Step struct {
	Goal   string         `yaml:"goal,omitempty" json:"goal,omitempty" jsonschema:"description=Agentic: the browser agent acts until the goal is met."`
	Expect string         `yaml:"expect,omitempty" json:"expect,omitempty" jsonschema:"description=Agentic assertion against the current page."`
	Action map[string]any `yaml:"action,omitempty" json:"action,omitempty" jsonschema:"description=Deterministic: one of navigate click fill press wait screenshot eval_js assert_url assert_text mock a11y visual."`
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
	Goal     string       `yaml:"goal,omitempty" json:"goal,omitempty" jsonschema:"description=What the user wants; the simulated user pursues it after the script. A scenario with a script and no goal plays the script and stops."`
	MaxTurns int          `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
	StopWhen string       `yaml:"stop_when,omitempty" json:"stop_when,omitempty" jsonschema:"description=A plain-language stop condition judged after each agent turn."`
	Script   []ScriptStep `yaml:"script,omitempty" json:"script,omitempty" jsonschema:"description=Deterministic steps played before the simulated user takes over: say on any conversational target; wait_for and dtmf on sip and livekit."`
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

// ToolSequence is the tool_sequence assertion.
type ToolSequence struct {
	Calls []string `yaml:"calls" json:"calls" jsonschema:"required,description=Tool names in the order they must be called."`
	Exact bool     `yaml:"exact,omitempty" json:"exact,omitempty" jsonschema:"description=The run's tool calls must be exactly this list; default a subsequence."`
}

// Assertion is one check. Exactly one of the kind fields is set.
type Assertion struct {
	Judge         *Judge             `yaml:"judge,omitempty" json:"judge,omitempty"`
	Contains      *string            `yaml:"contains,omitempty" json:"contains,omitempty" jsonschema:"description=The agent transcript contains this text."`
	NotContains   *string            `yaml:"not_contains,omitempty" json:"not_contains,omitempty"`
	Regex         *string            `yaml:"regex,omitempty" json:"regex,omitempty" jsonschema:"description=A regular expression the agent transcript matches."`
	NotRegex      *string            `yaml:"not_regex,omitempty" json:"not_regex,omitempty" jsonschema:"description=A regular expression no agent turn matches."`
	JSONPath      *JSONPathAssertion `yaml:"json_path,omitempty" json:"json_path,omitempty" jsonschema:"description=rest only."`
	Status        *int               `yaml:"status,omitempty" json:"status,omitempty" jsonschema:"description=rest only: the HTTP status of every turn."`
	ToolCalled    *ToolCall          `yaml:"tool_called,omitempty" json:"tool_called,omitempty"`
	ToolNotCalled *ToolCall          `yaml:"tool_not_called,omitempty" json:"tool_not_called,omitempty"`
	ToolSequence  *ToolSequence      `yaml:"tool_sequence,omitempty" json:"tool_sequence,omitempty" jsonschema:"description=The agent called these tools in this order (a subsequence of its calls; exact for the whole list)."`
	DTMFReceived  *DTMFAssertion     `yaml:"dtmf_received,omitempty" json:"dtmf_received,omitempty" jsonschema:"description=sip and livekit audio: digits the agent sent."`
	CallEndedBy   StringList         `yaml:"call_ended_by,omitempty" json:"call_ended_by,omitempty" jsonschema:"description=sip: agent caller timeout error no_answer busy."`
	TransferredTo *Matches           `yaml:"transferred_to,omitempty" json:"transferred_to,omitempty" jsonschema:"description=sip: target of a SIP REFER."`
	Evaluator     *Evaluator         `yaml:"evaluator,omitempty" json:"evaluator,omitempty"`
	Metric        *MetricAssertion   `yaml:"metric,omitempty" json:"metric,omitempty"`
	Turns         *IntCompare        `yaml:"turns,omitempty" json:"turns,omitempty" jsonschema:"description=Number of agent turns."`
	Captured      *CapturedAssertion `yaml:"captured,omitempty" json:"captured,omitempty" jsonschema:"description=A value captured during the run exists and equals or matches."`
	Golden        *GoldenAssertion   `yaml:"golden,omitempty" json:"golden,omitempty" jsonschema:"description=The agent's turns against reference turns: a file next to the suite or inline turns; exact / similar / judge."`
	Custom        *string            `yaml:"custom,omitempty" json:"custom,omitempty" jsonschema:"description=A Go-registered assertion by name."`
	Pack          *string            `yaml:"pack,omitempty" json:"pack,omitempty" jsonschema:"description=A reusable assertion set: a built-in red-team pack (prompt_injection / pii_leakage / system_prompt_leakage) or one of the suite's packs; it expands into its assertions when the suite loads."`
	OnTurn        OnTurn             `yaml:"on_turn,omitempty" json:"on_turn,omitempty"`

	// From is the pack this assertion came from, set when a pack expands;
	// results are reported as <pack>/<assertion>. Not part of the format.
	From string `yaml:"-" json:"-"`
}

// Kinds lists the assertion keys that are set, in AssertionKinds order.
func (a Assertion) Kinds() []string {
	set := map[string]bool{
		"judge":           a.Judge != nil,
		"contains":        a.Contains != nil,
		"not_contains":    a.NotContains != nil,
		"regex":           a.Regex != nil,
		"not_regex":       a.NotRegex != nil,
		"json_path":       a.JSONPath != nil,
		"status":          a.Status != nil,
		"tool_called":     a.ToolCalled != nil,
		"tool_not_called": a.ToolNotCalled != nil,
		"tool_sequence":   a.ToolSequence != nil,
		"dtmf_received":   a.DTMFReceived != nil,
		"call_ended_by":   len(a.CallEndedBy) > 0,
		"transferred_to":  a.TransferredTo != nil,
		"evaluator":       a.Evaluator != nil,
		"metric":          a.Metric != nil,
		"turns":           a.Turns != nil,
		"captured":        a.Captured != nil,
		"golden":          a.Golden != nil,
		"custom":          a.Custom != nil,
		"pack":            a.Pack != nil,
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
