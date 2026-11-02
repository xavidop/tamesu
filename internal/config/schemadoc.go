package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
)

// definitionOrder is the reading order of the suite reference page; anything
// not listed follows alphabetically.
var definitionOrder = []string{
	"Target", "BrowserOptions", "BrowserMock", "BrowserAuth", "RESTRequest", "RESTResponse", "RESTSession", "Dispatch", "Trigger", "HTTPTrigger",
	"Test", "Case", "Capture", "Step", "Persona", "Behaviors", "Scenario", "ScriptStep",
	"Assertion", "Judge", "ToolCall", "ToolSequence", "Evaluator", "JSONPathAssertion", "DTMFAssertion", "Matches", "MetricAssertion", "IntCompare", "CapturedAssertion", "GoldenAssertion", "OnTurn", "StringList",
	"Hooks", "Hook", "Defaults", "Models", "Budgets", "Duration",
}

// examples are shown under each section of the reference.
var examples = map[string]string{
	"Suite": `suite: support
tags: [smoke]
vars: { base_url: "https://api.example.com" }
secrets: { API_TOKEN: env:API_TOKEN }
targets:
  api:
    type: rest
    url: "{{vars.base_url}}/chat"
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    response: { text: "$.reply" }
tests:
  - name: refund policy
    target: api
    persona: { name: Marta, style: polite }
    scenario: { goal: "Find out if shoes bought 40 days ago can be returned", max_turns: 4 }
    assert:
      - judge: "The agent states the 30-day return policy"
      - contains: "30 days"`,
	"Target": `targets:
  web:  { type: browser, url: "https://shop.example.com", browser: { viewport: 1280x800 } }
  api:  { type: rest, url: "https://api.example.com/chat", response: { text: "$.reply", done: "$.end_session" } }
  grpc: { type: grpc, url: "grpc://agent.internal:9090" }
  voice: { type: livekit, url: "${LIVEKIT_URL}", api_key: "{{secrets.LIVEKIT_API_KEY}}", api_secret: "{{secrets.LIVEKIT_API_SECRET}}", mode: audio }
  phone: { type: sip, direction: outbound, to: "+15550001", from: "+15550002" }
  rt:   { type: realtime, model: gpt-realtime, headers: { Authorization: "Bearer {{secrets.OPENAI_API_KEY}}" }, mode: text }
  bot:  { type: pipecat, url: "wss://bot.example.com/ws", mode: audio }`,
	"BrowserOptions": `browser:
  viewport: 1280x800
  locale: en-US
  recording: webm
  mocks:
    - { url: "**/api/recommendations", body: { items: [] } }`,
	"BrowserMock": `mocks:
  - { url: "**/api/payment", method: POST, status: 502, delay_ms: 2000 }
  - { url: "**/analytics.js", abort: true }`,
	"BrowserAuth": `auth: { storage_state: auth/customer.json }`,
	"RESTRequest": `request:
  body: { session_id: "{{session}}", message: "{{message}}" }
  open: { type: hello, session: "{{session}}" }        # websocket only`,
	"RESTResponse": `response:
  text: "$.reply"
  tool_calls: "$.tool_calls[*]"
  done: "$.end_session"
  turn_done: "$.done"                                    # websocket: the message that ends a turn
  idle: 2s`,
	"RESTSession": `session: { id: "{{uuid}}" }`,
	"Dispatch":    `dispatch: { agent_name: support-agent }`,
	"Trigger": `trigger:
  http: { url: "https://agent.example.com/callbacks", method: POST, body: { number: "{{sip.inbound_number}}" } }`,
	"HTTPTrigger": `http: { url: "https://agent.example.com/dial", method: POST, headers: { Authorization: "Bearer {{secrets.AGENT_TOKEN}}" } }`,
	"Test": `tests:
  - name: return window
    target: api
    tags: [returns]
    repeat: 3
    pass_threshold: 0.67
    vars: { days: "40" }
    capture:
      - { name: order_id, regex: "order (ORD-[0-9]+)" }
    persona: { name: Marta, style: polite, language: en }
    scenario: { goal: "Ask whether shoes bought {{vars.days}} days ago can be returned", max_turns: 4 }
    assert:
      - judge: "The agent states the 30-day return policy"
      - tool_called: lookup_policy
  - name: refund status
    target: api
    quarantine: { reason: "SUP-412 flaky since the payments migration", until: 2026-12-31 }
    scenario: { goal: "Ask where the refund for order 1234 is", max_turns: 3 }
    assert:
      - judge: "The agent gives a refund date or asks for the order"`,
	"Case": `cases:
  - { name: late, vars: { days: "40", expect: "not possible" } }
  - { name: early, vars: { days: "10", expect: "can return" } }`,
	"Capture": `capture:
  - { name: order_id, regex: "order (ORD-[0-9]+)" }
  - { name: eta, json_path: "$.eta" }`,
	"Step": `steps:
  - goal: "Log in as {{vars.user}} with password {{secrets.PASSWORD}}"
  - expect: "The account page shows the user's name"
  - action: { click: "#add-to-cart" }
  - action: { a11y: { impact: serious } }
  - action: { visual: { name: cart, mask: ["#date"] } }`,
	"Persona": `persona:
  name: Marta
  style: polite, slightly confused
  language: es-ES
  voice: es-ES-female
  behaviors: { interrupt_probability: 0.2, background_noise: cafe }`,
	"Behaviors": `behaviors:
  interrupt_probability: 0.2
  silence_after_turn_ms: [300, 900]
  background_noise: street
  background_noise_snr_db: 15
  line_quality: mobile_poor`,
	"Scenario": `scenario:
  goal: "Reach the billing department and ask about a double charge"
  max_turns: 6
  stop_when: "the agent has confirmed a refund or clearly refused one"
  script:
    - wait_for: "press 2 for billing"
    - dtmf: "2"
    - say: "I was charged twice for order {{captured.order_id}}"`,
	"ScriptStep": `script:
  - wait_for: "press 2 for billing"
  - dtmf: "2"
  - say: "Hello, I was charged twice"`,
	"Assertion": `assert:
  - judge: "The agent states the 30-day return policy and does not promise an exception"
  - judge: { rubric: "The tone stays warm", scale: "1-5", min: 4 }
  - contains: "30 days"
  - not_regex: "[0-9]{16}"
  - tool_called: { name: lookup_policy, args: { topic: returns } }
  - tool_sequence: { calls: [lookup_policy, end_session] }
  - metric: { name: turn_latency_ms_p95, lt: 3000 }
  - turns: { lte: 6 }
  - captured: { name: order_id, matches: "^ORD-" }
  - golden: { file: golden/refund.json, mode: similar, min: 0.8 }
  - pack: pii_leakage
  - { contains: "goodbye", on_turn: last }`,
	"Judge":             `judge: { rubric: "The agent apologises before explaining the policy", scale: "1-5", min: 4, model: anthropic/claude-sonnet-4-6 }`,
	"ToolCall":          `tool_called: { name: lookup_policy, args: { topic: returns } }`,
	"ToolSequence":      `tool_sequence: { calls: [lookup_policy, issue_refund], exact: false }`,
	"Evaluator":         `evaluator: { name: genkitEval/faithfulness, min: 0.8 }`,
	"JSONPathAssertion": `json_path: { path: "$.status", equals: ok }`,
	"DTMFAssertion":     `dtmf_received: { equals: "9" }`,
	"Matches":           `transferred_to: { matches: "^sip:billing@" }`,
	"MetricAssertion":   `metric: { name: time_to_first_audio_ms_p95, lt: 1500 }`,
	"IntCompare":        `turns: { lte: 6 }`,
	"CapturedAssertion": `captured: { name: order_id, matches: "^ORD-[0-9]+$" }`,
	"GoldenAssertion":   `golden: { file: golden/refund.json, mode: judge }`,
	"OnTurn": `- { contains: "goodbye", on_turn: last }
- { json_path: { path: "$.end_session", equals: true }, on_turn: 3 }`,
	"StringList": `call_ended_by: [agent, caller]`,
	"Hooks": `hooks:
  before_all: [ { command: ./scripts/seed.sh } ]
  before_each: [ { http: { url: "{{vars.admin_url}}/reset", method: POST } } ]
  after_each: [ { command: "./scripts/cleanup.sh $TAMESU_SESSION", timeout: 30s } ]`,
	"Hook": `- command: ./scripts/seed.sh
- http: { url: "https://admin.example.com/reset", method: POST, headers: { Authorization: "Bearer {{secrets.ADMIN_TOKEN}}" } }`,
	"Defaults": `defaults:
  timeout: 3m
  repeat: 1
  retries: 1
  pass_threshold: 1
  assert:
    - pack: pii_leakage
  slo:
    turn_latency_ms_p95: 3000
    llm_cost_usd: 0.05`,
	"Models": `models:
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  simulator: openai/gpt-4.1-mini`,
	"Budgets":  `budgets: { max_cost_usd: 2, max_tokens: 500000 }`,
	"Duration": `timeout: 90s`,
}

func writeExample(b *strings.Builder, name string) {
	if ex, ok := examples[name]; ok {
		fmt.Fprintf(b, "Example:\n\n```yaml\n%s\n```\n\n", ex)
	}
}

// SuitesMarkdown renders the suite schema as the docs page
// site/src/pages/docs/suites.md. `make schema` writes it; the page is
// generated so the YAML reference can never drift from the Go structs.
func SuitesMarkdown() []byte {
	s := SuiteSchema()
	var b strings.Builder
	b.WriteString("---\nlayout: ../../layouts/DocsLayout.astro\ntitle: Suites (YAML reference)\n---\n\n")
	b.WriteString("<!-- Generated by `make schema`; edit the config types and their jsonschema tags, not this file. -->\n\n")
	b.WriteString("# Suites: the YAML reference\n\n")
	b.WriteString("A suite is one YAML file with the targets under test, the suite's defaults and its\n")
	b.WriteString("tests. This page lists every key, always in step with what `tamesu validate` accepts.\n")
	b.WriteString("The JSON Schema for editors is\n")
	b.WriteString("published at [`schema/tamesu.schema.json`](https://github.com/xavidop/tamesu/blob/main/schema/tamesu.schema.json);\n")
	b.WriteString("point your editor's YAML extension at it for completion and inline errors. See\n")
	b.WriteString("[Concepts](/docs/concepts/) for the model and [Configuration](/docs/configuration/) for `tamesu.yaml`.\n\n")
	b.WriteString("Strings accept `${VAR}` and `${VAR:-default}` (environment, non-secret values) and the\n")
	b.WriteString("templates `{{uuid}}`, `{{now}}`, `{{env.X}}`, `{{vars.x}}`, `{{secrets.X}}` and `{{faker.email}}`;\n")
	b.WriteString("REST bodies also take `{{session}}` and `{{message}}`. Durations are strings such as `30s`, `3m` or `1h30m`.\n\n")
	b.WriteString("## Suite\n\nThe top level of the file.\n\n")
	writeProperties(&b, s)
	writeExample(&b, "Suite")

	names := make([]string, 0, len(s.Definitions))
	for name := range s.Definitions {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ii, ij := indexOf(definitionOrder, names[i]), indexOf(definitionOrder, names[j])
		if ii != ij {
			return ii < ij
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		def := s.Definitions[name]
		fmt.Fprintf(&b, "## %s\n\n", name)
		if def.Description != "" {
			b.WriteString(def.Description + "\n\n")
		}
		switch {
		case len(def.OneOf) == 2 && def.OneOf[1].Properties != nil:
			str := def.OneOf[0].Description
			if str == "" {
				str = "a string"
			} else {
				str = "a string (" + strings.TrimSuffix(str, ".") + ")"
			}
			fmt.Fprintf(&b, "Either %s, or an object with:\n\n", str)
			writeProperties(&b, def.OneOf[1])
		case len(def.OneOf) > 0:
			var forms []string
			for _, o := range def.OneOf {
				forms = append(forms, typeString(o))
			}
			fmt.Fprintf(&b, "One of: %s.\n\n", strings.Join(forms, ", "))
		case def.Properties != nil && def.Properties.Len() > 0:
			writeProperties(&b, def)
		default:
			fmt.Fprintf(&b, "Type: %s.\n\n", typeString(def))
		}
		writeExample(&b, name)
	}
	return []byte(b.String())
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return len(list)
}

func writeProperties(b *strings.Builder, s *jsonschema.Schema) {
	if s.Properties == nil {
		return
	}
	b.WriteString("| Key | Type | Required | Description |\n|---|---|---|---|\n")
	for pair := s.Properties.Oldest(); pair != nil; pair = pair.Next() {
		required := ""
		for _, r := range s.Required {
			if r == pair.Key {
				required = "yes"
			}
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", pair.Key, typeString(pair.Value), required, strings.ReplaceAll(pair.Value.Description, "|", "\\|"))
	}
	b.WriteString("\n")
}

// typeString renders a property's type for people: a link to a definition,
// `list of X`, `map of string to X`, an enum, or a plain type.
func typeString(p *jsonschema.Schema) string {
	if p == nil {
		return "any"
	}
	if p.Ref != "" {
		name := strings.TrimPrefix(p.Ref, "#/$defs/")
		return fmt.Sprintf("[%s](#%s)", name, strings.ToLower(name))
	}
	if len(p.OneOf) > 0 {
		var forms []string
		for _, o := range p.OneOf {
			forms = append(forms, typeString(o))
		}
		return strings.Join(forms, " or ")
	}
	if len(p.Enum) > 0 {
		var vals []string
		for _, v := range p.Enum {
			vals = append(vals, fmt.Sprintf("`%v`", v))
		}
		return "one of " + strings.Join(vals, ", ")
	}
	switch p.Type {
	case "array":
		return "list of " + typeString(p.Items)
	case "object":
		if p.AdditionalProperties != nil && (p.AdditionalProperties.Type != "" || p.AdditionalProperties.Ref != "") {
			return "map of string to " + typeString(p.AdditionalProperties)
		}
		if p.Properties != nil && p.Properties.Len() > 0 {
			return "object"
		}
		return "object (any keys)"
	case "":
		return "any"
	}
	return p.Type
}
