// Package generate turns the scenario generator's draft into a suite file a
// person reviews: a target stub for the named target and one test per draft,
// in the order and shape a hand-written suite has.
package generate

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
)

// Options name the suite and its target.
type Options struct {
	// Suite is the suite name (default: from the source file name).
	Suite      string
	SourceName string
	Target     string
	// Type is rest, livekit, sip or browser.
	Type string
}

// Suite renders the draft as YAML.
func Suite(d model.Draft, o Options) ([]byte, error) {
	if o.Target == "" {
		return nil, fmt.Errorf("generate: a target name is required")
	}
	typ := o.Type
	if typ == "" {
		typ = config.TargetREST
	}
	if !contains(config.TargetTypes(), typ) {
		return nil, fmt.Errorf("generate: unknown target type %q (one of %s)", typ, strings.Join(config.TargetTypes(), ", "))
	}
	if len(d.Tests) == 0 {
		return nil, fmt.Errorf("generate: the model drafted no tests")
	}
	name := o.Suite
	if name == "" {
		name = slug(strings.TrimSuffix(filepath.Base(o.SourceName), filepath.Ext(o.SourceName)))
	}
	if name == "" {
		name = "generated"
	}
	root := mapping()
	root.Content = append(root.Content, key("suite"), scalar(name))
	if typ == config.TargetBrowser {
		root.Content = append(root.Content, key("vars"), mapping(key("user"), scalar("tester")))
	}
	secrets := mapping()
	switch typ {
	case config.TargetBrowser:
		secrets.Content = append(secrets.Content, key("TEST_PASSWORD"), scalar("env:TEST_PASSWORD"))
	case config.TargetLiveKit:
		secrets.Content = append(secrets.Content, key("LIVEKIT_API_KEY"), scalar("env:LIVEKIT_API_KEY"), key("LIVEKIT_API_SECRET"), scalar("env:LIVEKIT_API_SECRET"))
	case config.TargetRealtime:
		secrets.Content = append(secrets.Content, key("OPENAI_API_KEY"), scalar("env:OPENAI_API_KEY"))
	}
	if len(secrets.Content) > 0 {
		root.Content = append(root.Content, key("secrets"), secrets)
	}
	root.Content = append(root.Content, key("targets"), mapping(key(o.Target), targetStub(typ)))
	tests := &yaml.Node{Kind: yaml.SequenceNode}
	seen := map[string]int{}
	for i, t := range d.Tests {
		tests.Content = append(tests.Content, testNode(t, o.Target, typ, i, seen))
	}
	root.Content = append(root.Content, key("tests"), tests)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Drafted by tamesu generate from %s: review every test, then run it.\n", filepath.Base(o.SourceName))
	buf.WriteString("# The target is a stub: point url (and the request/response mapping) at your agent.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func targetStub(typ string) *yaml.Node {
	switch typ {
	case config.TargetREST:
		return mapping(
			key("type"), scalar("rest"),
			key("url"), scalar("${AGENT_URL}"),
			key("method"), scalar("POST"),
			key("request"), mapping(key("body"), mapping(key("session_id"), scalar("{{session}}"), key("message"), scalar("{{message}}"))),
			key("response"), mapping(key("text"), scalar("$.reply"), key("tool_calls"), scalar("$.tool_calls[*].name"), key("done"), scalar("$.end_session")),
		)
	case config.TargetLiveKit:
		return mapping(
			key("type"), scalar("livekit"),
			key("url"), scalar("${LIVEKIT_URL}"),
			key("api_key"), scalar("{{secrets.LIVEKIT_API_KEY}}"),
			key("api_secret"), scalar("{{secrets.LIVEKIT_API_SECRET}}"),
			key("room"), scalar("tamesu-{{uuid}}"),
			key("mode"), scalar("text"),
		)
	case config.TargetGRPC:
		return mapping(
			key("type"), scalar("grpc"),
			key("url"), scalar("${AGENT_GRPC_URL:-grpc://localhost:9090}"),
		)
	case config.TargetPipecat:
		return mapping(
			key("type"), scalar("pipecat"),
			key("url"), scalar("${AGENT_WS_URL:-ws://localhost:8765/ws}"),
			key("mode"), scalar("text"),
		)
	case config.TargetRealtime:
		return mapping(
			key("type"), scalar("realtime"),
			key("url"), scalar("wss://api.openai.com/v1/realtime"),
			key("model"), scalar("gpt-realtime"),
			key("headers"), mapping(key("Authorization"), scalar("Bearer {{secrets.OPENAI_API_KEY}}")),
			key("session_update"), mapping(key("instructions"), scalar("You are the agent under test.")),
			key("mode"), scalar("text"),
		)
	case config.TargetModel:
		return mapping(
			key("type"), scalar("model"),
			key("model"), scalar("googleai/gemini-2.5-flash"),
			key("system_file"), scalar("prompts/system.md"),
			key("tools"), &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{mapping(
				key("name"), scalar("end_session"),
				key("description"), scalar("Call when the user has no more questions"),
			)}},
		)
	case config.TargetTwilio:
		return mapping(
			key("type"), scalar("twilio"),
			key("url"), scalar("${AGENT_STREAM_URL:-wss://localhost:8080/media}"),
			key("parameters"), mapping(key("caller_tier"), scalar("gold")),
		)
	case config.TargetSIP:
		return mapping(
			key("type"), scalar("sip"),
			key("direction"), scalar("outbound"),
			key("to"), scalar("+15550000000"),
			key("ring_timeout"), scalar("30s"),
			key("max_call_duration"), scalar("5m"),
		)
	default:
		return mapping(
			key("type"), scalar("browser"),
			key("url"), scalar("${APP_URL}"),
			key("browser"), mapping(key("engine"), scalar("chromium"), key("viewport"), scalar("1280x800")),
		)
	}
}

func testNode(t model.DraftTest, target, typ string, i int, seen map[string]int) *yaml.Node {
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = fmt.Sprintf("test %d", i+1)
	}
	seen[name]++
	if n := seen[name]; n > 1 {
		name = fmt.Sprintf("%s (%d)", name, n)
	}
	n := mapping(key("name"), scalar(name), key("target"), scalar(target))
	if tags := cleanTags(t.Tags); len(tags) > 0 {
		n.Content = append(n.Content, key("tags"), flowSeq(tags))
	}
	if typ == config.TargetBrowser {
		steps := &yaml.Node{Kind: yaml.SequenceNode}
		for _, g := range t.Steps {
			if strings.TrimSpace(g) != "" {
				steps.Content = append(steps.Content, mapping(key("goal"), scalar(g)))
			}
		}
		for _, e := range t.Expects {
			if strings.TrimSpace(e) != "" {
				steps.Content = append(steps.Content, mapping(key("expect"), scalar(e)))
			}
		}
		if len(steps.Content) == 0 {
			goal := t.Goal
			if goal == "" {
				goal = "TODO: describe the goal"
			}
			steps.Content = append(steps.Content, mapping(key("goal"), scalar(goal)))
		}
		n.Content = append(n.Content, key("steps"), steps)
		if asserts := assertions(t, typ); len(asserts.Content) > 0 {
			n.Content = append(n.Content, key("assert"), asserts)
		}
		return n
	}
	persona := mapping()
	pname := t.Persona
	if pname == "" {
		pname = "Customer"
	}
	persona.Content = append(persona.Content, key("name"), scalar(pname))
	if t.Style != "" {
		persona.Content = append(persona.Content, key("style"), scalar(t.Style))
	}
	if t.Language != "" {
		persona.Content = append(persona.Content, key("language"), scalar(t.Language))
	}
	n.Content = append(n.Content, key("persona"), flowMap(persona))
	goal := t.Goal
	if goal == "" {
		goal = "TODO: describe what the user wants"
	}
	scenario := mapping(key("goal"), scalar(goal))
	maxTurns := t.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}
	scenario.Content = append(scenario.Content, key("max_turns"), intScalar(maxTurns))
	if t.StopWhen != "" {
		scenario.Content = append(scenario.Content, key("stop_when"), scalar(t.StopWhen))
	}
	n.Content = append(n.Content, key("scenario"), scenario)
	asserts := assertions(t, typ)
	if len(asserts.Content) == 0 {
		asserts.Content = append(asserts.Content, mapping(key("judge"), scalar("TODO: what must the agent do or never do?")))
	}
	asserts.Content = append(asserts.Content, mapping(key("turns"), flowMap(mapping(key("lte"), intScalar(maxTurns)))))
	n.Content = append(n.Content, key("assert"), asserts)
	return n
}

func assertions(t model.DraftTest, typ string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, j := range t.Judges {
		if strings.TrimSpace(j) != "" {
			seq.Content = append(seq.Content, mapping(key("judge"), scalar(j)))
		}
	}
	for _, c := range t.Contains {
		if strings.TrimSpace(c) != "" {
			seq.Content = append(seq.Content, mapping(key("contains"), scalar(c)))
		}
	}
	for _, c := range t.NotContains {
		if strings.TrimSpace(c) != "" {
			seq.Content = append(seq.Content, mapping(key("not_contains"), scalar(c)))
		}
	}
	return seq
}

var tagRe = regexp.MustCompile(`[^a-z0-9-]+`)

func cleanTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		t = strings.Trim(tagRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(t)), "-"), "-")
		if t != "" && !contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func slug(s string) string {
	return strings.Trim(tagRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// yaml.Node helpers.
func key(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }

func scalar(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: s}
	if strings.ContainsAny(s, "{}[]:#\"'\n") || strings.HasPrefix(s, "$") || strings.HasPrefix(s, "+") || s == "" {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func intScalar(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(i)}
}

func mapping(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: kv}
}

func flowMap(n *yaml.Node) *yaml.Node { n.Style = yaml.FlowStyle; return n }

func flowSeq(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, it := range items {
		n.Content = append(n.Content, scalar(it))
	}
	return n
}
