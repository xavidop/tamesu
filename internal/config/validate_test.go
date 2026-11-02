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
