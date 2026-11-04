package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

func TestValidateInvalidSuites(t *testing.T) {
	env := lookupFrom(map[string]string{"LIVEKIT_API_KEY": "k"})
	tests := []struct {
		file    string
		line    int
		msg     string
		path    string
		warning bool
	}{
		{file: "unknown-field.yaml", line: 7, msg: "field nmae not found in type Test"},
		{file: "missing-target.yaml", line: 8, msg: `target "nope" is not declared in targets (have api)`, path: `tests[0] "refund".target`},
		{file: "two-assertion-kinds.yaml", line: 9, msg: "an assertion is exactly one of", path: `tests[0] "t".assert[0]`},
		{file: "two-assertion-kinds.yaml", line: 9, msg: "(have contains, regex)"},
		{file: "metric-no-comparator.yaml", line: 9, msg: "metric needs exactly one of lt, lte, gt, gte, eq (have none)", path: `tests[0] "t".assert[0].metric`},
		{file: "step-two-kinds.yaml", line: 8, msg: "a step is exactly one of goal, expect or action (have goal, expect)", path: `tests[0] "t".steps[0]`},
		{file: "rest-only-assertion.yaml", line: 11, msg: "status applies to rest targets only; this target is livekit"},
		{file: "repeat-zero.yaml", line: 7, msg: "repeat must be 1 or more", path: `tests[0] "t".repeat`},
		{file: "not-a-suite.yaml", line: 1, msg: `not a tamesu suite (no "suite:" key)`},
		{file: "undeclared-secret.yaml", line: 6, msg: "secret API_TOKEN is not declared", path: "targets.api.headers.Authorization"},
		{file: "secret-via-env.yaml", line: 8, msg: "use {{secrets.NAME}} instead of ${LIVEKIT_API_KEY} for api_key", warning: true},
		{file: "ws-no-done.yaml", line: 7, msg: "streaming websocket without response.turn_done or response.done", path: "targets.ws.response", warning: true},
		{file: "realtime-bad.yaml", line: 6, msg: "protocol must be one of ga, beta", path: "targets.rt.protocol"},
		{file: "realtime-bad.yaml", line: 7, msg: "streaming applies to rest targets only; this target is realtime", warning: true},
		{file: "realtime-bad.yaml", line: 3, msg: "the OpenAI Realtime API needs headers.Authorization", warning: true},
		{file: "browser-without-steps.yaml", line: 5, msg: "browser tests need steps"},
		{file: "browser-without-steps.yaml", line: 7, msg: "persona applies to conversational targets"},
		{file: "sip-inbound-no-trigger.yaml", line: 3, msg: "inbound sip target needs trigger.http or trigger.command"},
		{file: "duplicate-test.yaml", line: 8, msg: `duplicate test name "same" (also tests[0])`},
		{file: "bad-secret-ref.yaml", line: 3, msg: "invalid secret ref"},
		{file: "syntax.yaml", line: 2, msg: "did not find expected"},
		{file: "model-fallback.yaml", line: 3, msg: "attempts cannot be negative", path: "models.retry.attempts"},
		{file: "model-fallback.yaml", line: 4, msg: "model references look like provider/model", path: "models.fallback.judge[0]", warning: true},
		{file: "model-fallback-target.yaml", line: 7, msg: "model references look like provider/model", path: "targets.prompt.fallback[0]", warning: true},
		{file: "model-fallback-target.yaml", line: 12, msg: "fallback applies to model and mcp targets only; this target is rest", warning: true},
		{file: "semantic-and-schema.yaml", line: 7, msg: "attempts cannot be negative", path: "targets.api.connect.attempts"},
		{file: "semantic-and-schema.yaml", line: 14, msg: "similar_to.min is a cosine similarity between 0 and 1", path: `tests[0] "t".assert[0].similar_to.min`},
		{file: "semantic-and-schema.yaml", line: 15, msg: "similar_to needs a text", path: `tests[0] "t".assert[1].similar_to`},
		{file: "semantic-and-schema.yaml", line: 16, msg: "not a valid JSON Schema", path: `tests[0] "t".assert[2].tool_called.schema`},
		{file: "semantic-and-schema.yaml", line: 17, msg: "schema applies to tool_called only", path: `tests[0] "t".assert[3].tool_not_called.schema`, warning: true},
		{file: "languages-adversarial.yaml", line: 10, msg: "a language is empty", path: `tests[0] "t".languages[1]`},
		{file: "languages-adversarial.yaml", line: 11, msg: "a tactic is empty", path: `tests[0] "t".persona.tactics[1]`},
		{file: "languages-adversarial.yaml", line: 11, msg: "an adversarial persona needs scenario.goal", path: `tests[0] "t".persona.adversarial`},
		{file: "languages-adversarial.yaml", line: 17, msg: "tactics applies to an adversarial persona only", path: `tests[1] "u".persona.tactics`, warning: true},
		{file: "handoff-bad.yaml", line: 6, msg: "invalid regex", path: `targets.api.handoff.regex`},
		{file: "handoff-bad.yaml", line: 10, msg: "handoff applies to conversational targets", path: `targets.web.handoff`},
		{file: "handoff-bad.yaml", line: 14, msg: "handoff needs one of tool, json_path or regex", path: `targets.bare.handoff`},
		{file: "handoff-bad.yaml", line: 20, msg: "invalid regex: error parsing regexp: missing closing ]", path: `tests[0] "t".assert[0].handoff.to`},
		{file: "handoff-bad.yaml", line: 20, msg: "within_turns cannot be negative", path: `tests[0] "t".assert[0].handoff.within_turns`},
		{file: "handoff-bad.yaml", line: 21, msg: "expected: false with other fields", path: `tests[0] "t".assert[1].handoff`, warning: true},
		{file: "handoff-bad.yaml", line: 26, msg: "handoff applies to conversational tests", path: `tests[1] "w".assert[0].handoff`},
		{file: "fuzz-bad.yaml", line: 9, msg: "fuzz_tools applies to targets that answer tool calls (realtime, pipecat, model, mcp); this target is rest", path: `tests[0] "rest".fuzz_tools`},
		{file: "fuzz-bad.yaml", line: 14, msg: `unknown fuzz mode "explode"`, path: `tests[1] "modes".fuzz_tools.lookup_order`},
		{file: "fuzz-bad.yaml", line: 15, msg: "mode delay needs a delay", path: `tests[1] "modes".fuzz_tools.get_balance.delay`},
		{file: "fuzz-bad.yaml", line: 16, msg: "mode custom needs a result", path: `tests[1] "modes".fuzz_tools.refund.result`},
		{file: "fuzz-bad.yaml", line: 17, msg: "delay cannot be negative", path: `tests[1] "modes".fuzz_tools.cancel.delay`},
		{file: "state-bad.yaml", line: 9, msg: "http needs a url", path: `tests[0] "t".assert[0].http`},
		{file: "state-bad.yaml", line: 9, msg: "method must be one of GET, POST, PUT, PATCH, DELETE, HEAD", path: `tests[0] "t".assert[0].http.method`},
		{file: "state-bad.yaml", line: 9, msg: "json_path needs a path", path: `tests[0] "t".assert[0].http.json_path`},
		{file: "state-bad.yaml", line: 9, msg: "invalid regex", path: `tests[0] "t".assert[0].http.regex`},
		{file: "state-bad.yaml", line: 9, msg: "status must be an HTTP status code", path: `tests[0] "t".assert[0].http.status`},
		{file: "state-bad.yaml", line: 10, msg: "on_turn does not apply to http", path: `tests[0] "t".assert[1].on_turn`},
		{file: "state-bad.yaml", line: 11, msg: "command needs run", path: `tests[0] "t".assert[2].command`},
		{file: "state-bad.yaml", line: 11, msg: "invalid regex: error parsing regexp: missing closing ]", path: `tests[0] "t".assert[2].command.regex`},
		{file: "state-bad.yaml", line: 11, msg: "exit must be between 0 and 255", path: `tests[0] "t".assert[2].command.exit`},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.msg, func(t *testing.T) {
			path := filepath.Join("testdata", "invalid", tt.file)
			_, errs, err := LoadSuite(path, LoadOptions{Env: env})
			require.NoError(t, err)
			var found *Error
			for _, e := range errs {
				if assert.ObjectsAreEqual(tt.warning, e.Warning) && regexp.MustCompile(regexp.QuoteMeta(tt.msg)).MatchString(e.Msg) {
					found = e
					break
				}
			}
			require.NotNil(t, found, "no entry with %q in:\n%s", tt.msg, errs)
			assert.Equal(t, tt.line, found.Line, "line of %q", found.Error())
			assert.Equal(t, path, found.File)
			if tt.path != "" {
				assert.Equal(t, tt.path, found.Path)
			}
			if !tt.warning {
				assert.True(t, errs.HasErrors())
			}
		})
	}
}

func TestValidateSecretViaEnvIsOnlyAWarning(t *testing.T) {
	env := lookupFrom(map[string]string{"LIVEKIT_API_KEY": "k"})
	s, errs, err := LoadSuite(filepath.Join("testdata", "invalid", "secret-via-env.yaml"), LoadOptions{Env: env})
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.False(t, errs.HasErrors(), "%s", errs)
	require.Len(t, errs, 1)
	assert.Equal(t, "k", s.Targets["voice"].APIKey)
}

func TestValidateWithoutNodeHasNoLines(t *testing.T) {
	s := &Suite{Suite: "x", Path: "mem.yaml", Targets: map[string]Target{"api": {Type: TargetREST, URL: "http://x", Response: &RESTResponse{Text: "$.a"}}}, Tests: []Test{{Name: "t", Target: "api", Scenario: &Scenario{Goal: "g"}, Assert: []Assertion{{Regex: ptr("(")}}}}}
	errs := Validate(s, DefaultProject(), nil)
	require.Len(t, errs, 1)
	assert.Equal(t, 0, errs[0].Line)
	assert.Equal(t, `mem.yaml: tests[0] "t".assert[0].regex: invalid regex: error parsing regexp: missing closing ): `+"`(`", errs[0].Error())
}

func TestSelectTests(t *testing.T) {
	s, errs, err := LoadSuite(filepath.Join("testdata", "valid", "support-agent.yaml"), LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.False(t, errs.HasErrors())
	names := func(ts []Test) []string {
		var out []string
		for _, x := range ts {
			out = append(out, x.Name)
		}
		return out
	}
	assert.Len(t, s.SelectTests(nil, nil, ""), 4)
	assert.Equal(t, []string{"refund policy question"}, names(s.SelectTests(regexp.MustCompile("refund"), nil, "")))
	assert.Equal(t, []string{"reschedule booking by voice"}, names(s.SelectTests(nil, nil, "voice")))
	// The suite tag applies to every test.
	assert.Len(t, s.SelectTests(nil, []string{"smoke"}, ""), 4)
	assert.Empty(t, s.SelectTests(nil, []string{"nightly"}, ""))
	assert.Equal(t, []string{"API_TOKEN"}, SecretsUsedBy(s, s.Tests[1]))
	assert.Equal(t, []string{"TEST_PASSWORD"}, SecretsUsedBy(s, s.Tests[0]))
	assert.ElementsMatch(t, []string{"TEST_PASSWORD", "API_TOKEN", "LIVEKIT_API_KEY", "LIVEKIT_API_SECRET"}, SecretsUsed(s))
}

func ptr[T any](v T) *T { return &v }
