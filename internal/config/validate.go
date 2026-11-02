package config

import (
	"github.com/xavidop/tamesu/target"

	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xavidop/mamori"
	"gopkg.in/yaml.v3"
)

var (
	viewportRe = regexp.MustCompile(`^\d+x\d+$`)
	e164Re     = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	// realtimeURLRe accepts the schemes a websocket handshake can use.
	realtimeURLRe = regexp.MustCompile(`^(wss?|https?)://`)
	e164PatRe     = regexp.MustCompile(`^\+[0-9*]+$`)
	dtmfRe        = regexp.MustCompile(`^[0-9A-Da-d*#]+$`)
)

// Enumerations the validator checks.
var (
	StreamingModes = []string{"none", "sse", "websocket"}
	VoiceModes     = []string{"text", "audio"}
	// TranscriptSources are the livekit audio-mode transcript_source values.
	TranscriptSources = []string{"auto", "agent", "stt"}
	SIPDirections     = []string{"outbound", "inbound"}
	JudgeScales       = []string{"binary", "1-5"}
	CallEnders        = []string{"agent", "caller", "timeout", "error", "no_answer", "busy"}
	LineQualities     = []string{"pstn", "mobile_poor", "voip_good"}
	ReporterNames     = []string{"console", "junit", "json", "html"}
	GenkitPlugins     = []string{"googlegenai", "anthropic", "openai", "ollama", "deepgram", "elevenlabs", "evaluators"}
	ScriptStepKinds   = []string{"wait_for", "dtmf", "say"}
	// SIPTransports are the sip.trunk.transport values.
	SIPTransports = []string{"auto", "udp", "tcp", "tls"}
)

// targetFieldTypes maps each type-specific target field to the type it
// belongs to, so a field on the wrong kind of target earns a warning.
var targetFieldTypes = map[string][]string{
	"browser": {TargetBrowser}, "auth": {TargetBrowser}, "allowed_domains": {TargetBrowser},
	"method": {TargetREST}, "headers": {TargetREST, TargetRealtime}, "request": {TargetREST}, "response": {TargetREST}, "streaming": {TargetREST}, "session": {TargetREST},
	"api_key": {TargetLiveKit}, "api_secret": {TargetLiveKit}, "room": {TargetLiveKit}, "dispatch": {TargetLiveKit},
	"agent_identity_prefix": {TargetLiveKit}, "mode": {TargetLiveKit, TargetRealtime}, "data_topic": {TargetLiveKit}, "join_timeout": {TargetLiveKit},
	"transcript_source": {TargetLiveKit},
	"direction":         {TargetSIP}, "to": {TargetSIP}, "from": {TargetSIP}, "trunk_id": {TargetSIP}, "ring_timeout": {TargetSIP},
	"max_call_duration": {TargetSIP}, "trigger": {TargetSIP},
	"model": {TargetRealtime}, "session_update": {TargetRealtime}, "protocol": {TargetRealtime}, "done_tool": {TargetRealtime}, "turn_timeout": {TargetRealtime},
}

// RealtimeProtocols are the event dialects of the realtime target.
var RealtimeProtocols = []string{"ga", "beta"}

type validator struct {
	file string
	s    *Suite
	p    Project
	root *yaml.Node
	errs Errors
}

func (v *validator) add(warn bool, path []string, format string, args ...any) {
	v.errs = append(v.errs, &Error{
		File:    v.file,
		Line:    lineOf(v.root, path...),
		Path:    describePath(v.s, path),
		Msg:     fmt.Sprintf(format, args...),
		Warning: warn,
	})
}

func (v *validator) errorf(path []string, format string, args ...any) {
	v.add(false, path, format, args...)
}
func (v *validator) warnf(path []string, format string, args ...any) {
	v.add(true, path, format, args...)
}

func at(parts ...string) []string { return parts }

func with(path []string, more ...string) []string {
	return append(append(make([]string, 0, len(path)+len(more)), path...), more...)
}

func idx(i int) string { return strconv.Itoa(i) }

// describePath renders a YAML path for people: tests[1] "name".assert[2].metric.
func describePath(s *Suite, path []string) string {
	var b strings.Builder
	for i, p := range path {
		if n, err := strconv.Atoi(p); err == nil {
			fmt.Fprintf(&b, "[%d]", n)
			if s != nil && i > 0 && path[i-1] == "tests" && n < len(s.Tests) && s.Tests[n].Name != "" {
				fmt.Fprintf(&b, " %q", s.Tests[n].Name)
			}
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

func listOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks a decoded suite against the rules of design.md §5 and
// returns every problem, warnings included. root is the parsed document for
// line numbers; it may be nil (lines are then 0).
func Validate(s *Suite, p Project, root *yaml.Node) Errors {
	v := &validator{file: s.Path, s: s, p: p, root: root}
	v.suite()
	return v.errs
}

func (v *validator) suite() {
	s := v.s
	if strings.TrimSpace(s.Suite) == "" {
		v.errorf(at("suite"), "suite name is required")
	}
	if len(s.Targets) == 0 {
		v.errorf(at("targets"), "a suite needs at least one target")
	}
	for _, name := range sortedKeys(s.Targets) {
		v.target(name, s.Targets[name])
	}
	for _, name := range sortedKeys(s.Secrets) {
		v.secretRef(at("secrets", name), s.Secrets[name])
	}
	if s.Defaults != nil {
		v.defaults(at("defaults"), *s.Defaults)
	}
	if s.Budgets != nil {
		v.budgets(at("budgets"), *s.Budgets)
	}
	if s.Models != nil {
		v.models(at("models"), *s.Models)
	}
	if len(s.Tests) == 0 {
		v.warnf(at("tests"), "the suite has no tests")
	}
	seen := map[string]int{}
	for i, t := range s.Tests {
		tp := at("tests", idx(i))
		switch j, dup := seen[t.Name]; {
		case t.Name == "":
			v.errorf(tp, "a test needs a name")
		case dup:
			v.errorf(with(tp, "name"), "duplicate test name %q (also tests[%d])", t.Name, j)
		default:
			seen[t.Name] = i
		}
		v.test(i, t)
	}
	v.secretsDeclared()
}

func (v *validator) target(name string, t Target) {
	tp := at("targets", name)
	switch t.Type {
	case "":
		v.errorf(tp, "target needs a type (one of %s)", strings.Join(TargetTypes(), ", "))
		return
	case TargetBrowser:
		if t.URL == "" {
			v.errorf(tp, "browser target needs url")
		}
		if t.Browser != nil && t.Browser.Viewport != "" && !viewportRe.MatchString(t.Browser.Viewport) {
			v.errorf(with(tp, "browser", "viewport"), "viewport must be WIDTHxHEIGHT such as 1280x800")
		}
	case TargetREST:
		if t.URL == "" {
			v.errorf(tp, "rest target needs url")
		}
		if t.Streaming != "" && !slices.Contains(StreamingModes, t.Streaming) {
			v.errorf(with(tp, "streaming"), "streaming must be one of %s", strings.Join(StreamingModes, ", "))
		}
		if t.Response == nil || t.Response.Text == "" {
			v.warnf(tp, "response.text is not set; the whole response body is read as the agent's reply")
		}
		if t.Streaming == "websocket" {
			if t.Response == nil || (t.Response.TurnDone == "" && t.Response.Done == "") {
				v.warnf(with(tp, "response"), "streaming websocket without response.turn_done or response.done: a turn ends only after response.idle (default 2s) without a message")
			}
		} else {
			if t.Request != nil && t.Request.Open != nil {
				v.warnf(with(tp, "request", "open"), "request.open applies to streaming websocket only")
			}
			if t.Response != nil && (t.Response.TurnDone != "" || t.Response.Idle != 0) {
				v.warnf(with(tp, "response"), "response.turn_done and response.idle apply to streaming websocket only")
			}
		}
	case TargetLiveKit:
		if t.URL == "" {
			v.errorf(tp, "livekit target needs url (the LiveKit server, wss://...)")
		}
		if t.Mode != "" && !slices.Contains(VoiceModes, t.Mode) {
			v.errorf(with(tp, "mode"), "mode must be text or audio")
		}
		if t.TranscriptSource != "" && !slices.Contains(TranscriptSources, t.TranscriptSource) {
			v.errorf(with(tp, "transcript_source"), "transcript_source must be one of %s", strings.Join(TranscriptSources, ", "))
		}
	case TargetRealtime:
		if t.Mode != "" && !slices.Contains(VoiceModes, t.Mode) {
			v.errorf(with(tp, "mode"), "mode must be text or audio")
		}
		if t.Protocol != "" && !slices.Contains(RealtimeProtocols, t.Protocol) {
			v.errorf(with(tp, "protocol"), "protocol must be one of %s", strings.Join(RealtimeProtocols, ", "))
		}
		if t.URL != "" && !strings.Contains(t.URL, "{{") && !strings.Contains(t.URL, "${") && !realtimeURLRe.MatchString(t.URL) {
			v.errorf(with(tp, "url"), "realtime url must be a websocket URL (ws://, wss://, http:// or https://)")
		}
		if _, ok := t.Headers["Authorization"]; !ok && (t.URL == "" || strings.Contains(t.URL, "api.openai.com")) {
			v.warnf(tp, "the OpenAI Realtime API needs headers.Authorization: \"Bearer {{secrets.OPENAI_API_KEY}}\"")
		}
	case TargetSIP:
		switch t.Direction {
		case "":
			v.errorf(tp, "sip target needs direction (outbound: tamesu calls the agent, inbound: the agent calls tamesu)")
		case "outbound":
			switch {
			case t.To == "":
				v.errorf(tp, "outbound sip target needs to (the agent's number)")
			case target.Templated(t.To):
				if len(v.p.SIP.AllowedNumbers) == 0 {
					v.errorf(with(tp, "to"), "to is a template, so sip.allowed_numbers must list the numbers tamesu may dial (a typo must not call a stranger)")
				}
			case !e164Re.MatchString(t.To):
				v.errorf(with(tp, "to"), "to must be an E.164 number such as +18005550100")
			case len(v.p.SIP.AllowedNumbers) > 0 && !target.NumberAllowed(t.To, v.p.SIP.AllowedNumbers):
				v.errorf(with(tp, "to"), "%s is not in sip.allowed_numbers (%s); tamesu refuses to dial it", t.To, strings.Join(v.p.SIP.AllowedNumbers, ", "))
			}
		case "inbound":
			if t.Trigger == nil || (t.Trigger.HTTP == nil && t.Trigger.Command == "") {
				v.errorf(tp, "inbound sip target needs trigger.http or trigger.command to make the agent call sip.inbound_number")
			} else if t.Trigger.HTTP != nil && t.Trigger.HTTP.URL == "" {
				v.errorf(with(tp, "trigger", "http"), "trigger.http needs url")
			}
		default:
			v.errorf(with(tp, "direction"), "direction must be outbound or inbound")
		}
		if t.From != "" && !strings.Contains(t.From, "{{") && !e164Re.MatchString(t.From) {
			v.errorf(with(tp, "from"), "from must be an E.164 number such as +34910000000")
		}
	default:
		v.errorf(with(tp, "type"), "unknown target type %q (one of %s)", t.Type, strings.Join(TargetTypes(), ", "))
		return
	}
	n := nodeAt(v.root, "targets", name)
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if want, ok := targetFieldTypes[key]; ok && !slices.Contains(want, t.Type) {
			v.warnf(with(tp, key), "%s applies to %s targets only; this target is %s", key, strings.Join(want, " and "), t.Type)
		}
		if key == "url" && t.Type == TargetSIP {
			v.warnf(with(tp, key), "url applies to browser, rest and livekit targets; sip uses sip.livekit_url")
		}
	}
}

func (v *validator) test(i int, t Test) {
	tp := at("tests", idx(i))
	target, ok := v.s.Targets[t.Target]
	switch {
	case t.Target == "":
		v.errorf(tp, "test needs a target")
	case !ok:
		v.errorf(with(tp, "target"), "target %q is not declared in targets (have %s)", t.Target, listOr(sortedKeys(v.s.Targets), "none"))
	}
	if t.Repeat != nil && *t.Repeat < 1 {
		v.errorf(with(tp, "repeat"), "repeat must be 1 or more")
	}
	if t.PassThreshold != nil && (*t.PassThreshold <= 0 || *t.PassThreshold > 1) {
		v.errorf(with(tp, "pass_threshold"), "pass_threshold must be above 0 and at most 1")
	}
	if t.Retries != nil && *t.Retries < 0 {
		v.errorf(with(tp, "retries"), "retries cannot be negative")
	}
	if t.Timeout != nil && *t.Timeout < 0 {
		v.errorf(with(tp, "timeout"), "timeout cannot be negative")
	}
	ttype := ""
	if ok {
		ttype = target.Type
	}
	mode := t.Mode
	if mode == "" {
		mode = target.Mode
	}
	if ttype == TargetSIP {
		mode = "audio"
	}
	if t.Mode != "" {
		if ok && ttype != TargetLiveKit && ttype != TargetSIP && ttype != TargetRealtime {
			v.errorf(with(tp, "mode"), "mode applies to livekit, sip and realtime tests only; %q is a %s target", t.Target, ttype)
		}
		if !slices.Contains(VoiceModes, t.Mode) {
			v.errorf(with(tp, "mode"), "mode must be text or audio")
		}
	}
	switch {
	case ttype == TargetBrowser:
		if len(t.Steps) == 0 {
			v.errorf(tp, "browser tests need steps")
		}
		if t.Persona != nil {
			v.errorf(with(tp, "persona"), "persona applies to conversational targets (rest, livekit, sip, realtime); %q is a browser target", t.Target)
		}
		if t.Scenario != nil {
			v.errorf(with(tp, "scenario"), "scenario applies to conversational targets (rest, livekit, sip, realtime); %q is a browser target", t.Target)
		}
	case ok:
		if t.Scenario == nil || strings.TrimSpace(t.Scenario.Goal) == "" {
			v.errorf(tp, "conversational tests need scenario.goal")
		}
		if len(t.Steps) > 0 {
			v.errorf(with(tp, "steps"), "steps apply to browser tests; %q is a %s target", t.Target, ttype)
		}
	}
	for j, st := range t.Steps {
		v.step(with(tp, "steps", idx(j)), st)
	}
	if t.Scenario != nil {
		v.scenario(with(tp, "scenario"), *t.Scenario, ttype)
	}
	if t.Persona != nil && t.Persona.Behaviors != nil {
		v.behaviors(with(tp, "persona", "behaviors"), *t.Persona.Behaviors)
	}
	for j, a := range t.Assert {
		v.assertion(with(tp, "assert", idx(j)), a, ttype, mode)
	}
}

func (v *validator) step(sp []string, st Step) {
	kinds := st.Kinds()
	if len(kinds) != 1 {
		v.errorf(sp, "a step is exactly one of goal, expect or action (have %s)", listOr(kinds, "none"))
		return
	}
	if st.Action == nil {
		return
	}
	keys := sortedKeys(st.Action)
	if len(keys) != 1 {
		v.errorf(with(sp, "action"), "an action is exactly one of %s (have %s)", strings.Join(ActionNames, ", "), listOr(keys, "none"))
		return
	}
	if !slices.Contains(ActionNames, keys[0]) {
		v.errorf(with(sp, "action", keys[0]), "unknown action %q (one of %s)", keys[0], strings.Join(ActionNames, ", "))
	}
}

func (v *validator) scenario(sp []string, sc Scenario, ttype string) {
	if sc.MaxTurns < 0 {
		v.errorf(with(sp, "max_turns"), "max_turns cannot be negative")
	}
	if len(sc.Script) > 0 && ttype != "" && ttype != TargetSIP && ttype != TargetLiveKit {
		v.warnf(with(sp, "script"), "script steps apply to sip and livekit tests; this target is %s", ttype)
	}
	for i, st := range sc.Script {
		p := with(sp, "script", idx(i))
		if st.Kind() == "" {
			v.errorf(p, "a script step is exactly one of %s", strings.Join(ScriptStepKinds, ", "))
			continue
		}
		if st.DTMF != "" && !dtmfRe.MatchString(st.DTMF) {
			v.errorf(with(p, "dtmf"), "dtmf must be digits, A-D, * or #")
		}
	}
}

func (v *validator) behaviors(bp []string, b Behaviors) {
	if b.InterruptProbability < 0 || b.InterruptProbability > 1 {
		v.errorf(with(bp, "interrupt_probability"), "interrupt_probability must be between 0 and 1")
	}
	if n := len(b.SilenceAfterTurnMs); n > 0 {
		switch {
		case n != 2:
			v.errorf(with(bp, "silence_after_turn_ms"), "silence_after_turn_ms is [min, max] in milliseconds")
		case b.SilenceAfterTurnMs[0] < 0 || b.SilenceAfterTurnMs[0] > b.SilenceAfterTurnMs[1]:
			v.errorf(with(bp, "silence_after_turn_ms"), "silence_after_turn_ms needs 0 <= min <= max")
		}
	}
	if b.LineQuality != "" && !slices.Contains(LineQualities, b.LineQuality) {
		v.errorf(with(bp, "line_quality"), "line_quality must be one of %s", strings.Join(LineQualities, ", "))
	}
	if n := b.BackgroundNoise; n != "" && !slices.Contains(NoisePresets, n) && !strings.HasSuffix(strings.ToLower(n), ".wav") {
		v.errorf(with(bp, "background_noise"), "background_noise must be one of %s or a .wav file", strings.Join(NoisePresets, ", "))
	}
	if b.BackgroundNoiseSNRdB != nil && (*b.BackgroundNoiseSNRdB < -10 || *b.BackgroundNoiseSNRdB > 60) {
		v.errorf(with(bp, "background_noise_snr_db"), "background_noise_snr_db must be between -10 and 60")
	}
}

func (v *validator) assertion(ap []string, a Assertion, ttype, mode string) {
	kinds := a.Kinds()
	if len(kinds) != 1 {
		v.errorf(ap, "an assertion is exactly one of %s (have %s)", strings.Join(AssertionKinds, ", "), listOr(kinds, "none"))
		return
	}
	kind := kinds[0]
	kp := with(ap, kind)
	restOnly := func() {
		if ttype != "" && ttype != TargetREST {
			v.errorf(kp, "%s applies to rest targets only; this target is %s", kind, ttype)
		}
	}
	sipOnly := func() {
		if ttype != "" && ttype != TargetSIP {
			v.errorf(kp, "%s applies to sip targets only; this target is %s", kind, ttype)
		}
	}
	switch kind {
	case "judge":
		if strings.TrimSpace(a.Judge.Rubric) == "" {
			v.errorf(kp, "judge needs a rubric")
		}
		if a.Judge.Scale != "" && !slices.Contains(JudgeScales, a.Judge.Scale) {
			v.errorf(with(kp, "scale"), "judge scale must be binary or 1-5")
		}
		if a.Judge.Min != 0 && a.Judge.Scale != "1-5" {
			v.warnf(with(kp, "min"), "min applies to scale 1-5")
		}
	case "regex":
		if _, err := regexp.Compile(*a.Regex); err != nil {
			v.errorf(kp, "invalid regex: %v", err)
		}
	case "json_path":
		restOnly()
		if a.JSONPath.Path == "" {
			v.errorf(kp, "json_path needs path")
		}
	case "status":
		restOnly()
		if *a.Status < 100 || *a.Status > 599 {
			v.errorf(kp, "status must be an HTTP status code")
		}
	case "tool_called", "tool_not_called":
		var name string
		if kind == "tool_called" {
			name = a.ToolCalled.Name
		} else {
			name = a.ToolNotCalled.Name
		}
		if name == "" {
			v.errorf(kp, "%s needs a tool name", kind)
		}
	case "dtmf_received":
		if ttype != "" && ttype != TargetSIP && (ttype != TargetLiveKit || mode != "audio") {
			v.errorf(kp, "dtmf_received applies to sip tests and livekit tests in audio mode; this test is %s %s", ttype, mode)
		}
		if a.DTMFReceived.Equals == "" || !dtmfRe.MatchString(a.DTMFReceived.Equals) {
			v.errorf(kp, "dtmf_received.equals must be digits, A-D, * or #")
		}
	case "call_ended_by":
		sipOnly()
		for _, who := range a.CallEndedBy {
			if !slices.Contains(CallEnders, who) {
				v.errorf(kp, "call_ended_by values are %s, not %q", strings.Join(CallEnders, ", "), who)
			}
		}
	case "transferred_to":
		sipOnly()
		if _, err := regexp.Compile(a.TransferredTo.Matches); err != nil {
			v.errorf(kp, "invalid regex: %v", err)
		}
	case "evaluator":
		if a.Evaluator.Name == "" {
			v.errorf(kp, "evaluator needs a name such as genkitEval/faithfulness")
		}
		if a.Evaluator.Min < 0 || a.Evaluator.Min > 1 {
			v.errorf(with(kp, "min"), "evaluator min must be between 0 and 1")
		}
	case "metric":
		if a.Metric.Name == "" {
			v.errorf(kp, "metric needs a name")
		}
		if c := a.Metric.Comparators(); len(c) != 1 {
			v.errorf(kp, "metric needs exactly one of lt, lte, gt, gte, eq (have %s)", listOr(c, "none"))
		}
	case "turns":
		if c := a.Turns.Comparators(); len(c) != 1 {
			v.errorf(kp, "turns needs exactly one of lt, lte, gt, gte, eq (have %s)", listOr(c, "none"))
		}
	case "custom":
		if *a.Custom == "" {
			v.errorf(kp, "custom needs the name of a registered assertion")
		}
	}
	if a.OnTurn.IsSet() && ttype == TargetBrowser {
		v.errorf(with(ap, "on_turn"), "on_turn applies to conversational tests")
	}
}

// secretRef checks that a declared secret ref parses as a mamori ref (chains
// included). ${VAR} ref vars are replaced by a placeholder first.
func (v *validator) secretRef(sp []string, ref string) {
	if strings.TrimSpace(ref) == "" {
		v.errorf(sp, "secret ref is empty")
		return
	}
	probe := envRe.ReplaceAllString(ref, "x")
	if _, err := mamori.ParseRefs(probe); err != nil {
		v.errorf(sp, "invalid secret ref: %v", err)
	}
}

// secretsDeclared reports every {{secrets.X}} whose X is declared neither in
// the suite nor in the project.
func (v *validator) secretsDeclared() {
	declared := func(name string) bool {
		_, inSuite := v.s.Secrets[name]
		_, inProject := v.p.Secrets[name]
		return inSuite || inProject
	}
	if v.root == nil {
		for _, name := range SecretsUsed(v.s) {
			if !declared(name) {
				v.errorf(nil, "secret %s is not declared (add it under secrets: in the suite or tamesu.yaml)", name)
			}
		}
		return
	}
	walkScalars(v.root, nil, topLevelKeysNotExpanded, func(sc scalarRef) {
		for _, name := range SecretRefs(sc.node.Value) {
			if !declared(name) {
				v.errorf(sc.path, "secret %s is not declared (add it under secrets: in the suite or tamesu.yaml)", name)
			}
		}
	})
}

func (v *validator) defaults(dp []string, d Defaults) {
	if d.Repeat < 0 || (d.Repeat == 0 && v.s == nil) {
		v.errorf(with(dp, "repeat"), "repeat must be 1 or more")
	}
	if d.PassThreshold < 0 || d.PassThreshold > 1 || (d.PassThreshold == 0 && v.s == nil) {
		v.errorf(with(dp, "pass_threshold"), "pass_threshold must be above 0 and at most 1")
	}
	if d.Retries < 0 {
		v.errorf(with(dp, "retries"), "retries cannot be negative")
	}
	if d.Timeout < 0 {
		v.errorf(with(dp, "timeout"), "timeout cannot be negative")
	}
}

func (v *validator) budgets(bp []string, b Budgets) {
	if b.MaxCostUSD < 0 {
		v.errorf(with(bp, "max_cost_usd"), "max_cost_usd cannot be negative")
	}
	if b.MaxTokens < 0 {
		v.errorf(with(bp, "max_tokens"), "max_tokens cannot be negative")
	}
}

func (v *validator) models(mp []string, m Models) {
	for key, ref := range map[string]string{"default": m.Default, "judge": m.Judge, "browser_agent": m.BrowserAgent, "simulator": m.Simulator, "scenario_generator": m.ScenarioGenerator} {
		if ref != "" && !strings.Contains(ref, "/") {
			v.warnf(with(mp, key), "model references look like provider/model, such as googleai/gemini-2.5-flash")
		}
	}
}

// ValidateProject checks tamesu.yaml. root may be nil.
func ValidateProject(p Project, file string, root *yaml.Node) Errors {
	v := &validator{file: file, p: p, root: root}
	if p.Version != 1 {
		v.errorf(at("version"), "version must be 1")
	}
	if len(p.Suites) == 0 {
		v.warnf(at("suites"), "no suites configured; pass suite paths on the command line")
	}
	for i, r := range p.Reporters {
		if !slices.Contains(ReporterNames, r) {
			v.errorf(at("reporters", idx(i)), "unknown reporter %q (one of %s)", r, strings.Join(ReporterNames, ", "))
		}
	}
	for i, pl := range p.Genkit.Plugins {
		if !slices.Contains(GenkitPlugins, pl) {
			v.warnf(at("genkit", "plugins", idx(i)), "unknown Genkit plugin %q (known: %s)", pl, strings.Join(GenkitPlugins, ", "))
		}
	}
	v.defaults(at("defaults"), p.Defaults)
	v.budgets(at("budgets"), p.Budgets)
	v.models(at("models"), p.Models)
	if p.SIP.MaxConcurrentCalls < 1 {
		v.errorf(at("sip", "max_concurrent_calls"), "max_concurrent_calls must be 1 or more")
	}
	for i, pat := range p.SIP.AllowedNumbers {
		if !e164PatRe.MatchString(pat) {
			v.errorf(at("sip", "allowed_numbers", idx(i)), "allowed_numbers are E.164 patterns such as +1800555* (digits and * after the +)")
		}
	}
	if tr := p.SIP.Trunk; tr != nil {
		if tr.Address == "" {
			v.errorf(at("sip", "trunk", "address"), "sip.trunk.address is required (the provider's SIP host)")
		}
		if tr.Transport != "" && !slices.Contains(SIPTransports, tr.Transport) {
			v.errorf(at("sip", "trunk", "transport"), "transport must be one of %s", strings.Join(SIPTransports, ", "))
		}
		for i, n := range append(append([]string(nil), tr.Numbers...), tr.InboundNumbers...) {
			if !e164Re.MatchString(n) {
				v.errorf(at("sip", "trunk", "numbers", idx(i)), "trunk numbers are E.164 such as +34910000000")
			}
		}
	}
	if p.SIP.InboundNumber != "" && !e164Re.MatchString(p.SIP.InboundNumber) {
		v.errorf(at("sip", "inbound_number"), "inbound_number must be an E.164 number such as +34910000000")
	}
	if p.LiveKit.MaxConcurrentRooms < 1 {
		v.errorf(at("livekit", "max_concurrent_rooms"), "max_concurrent_rooms must be 1 or more")
	}
	for _, name := range sortedKeys(p.Secrets) {
		v.secretRef(at("secrets", name), p.Secrets[name])
	}
	return v.errs
}
