package cli_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/xavidop/mamori"

	"github.com/xavidop/tamesu/internal/cli"
)

func doctorProject(t *testing.T, secrets string) string {
	t.Helper()
	return writeProject(t, map[string]string{
		"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\nsecrets:\n" + secrets,
		"s/x.yaml":    minimalSuite,
	})
}

func TestDoctorOK(t *testing.T) {
	fake := &fakeProvider{values: map[string]string{"a": "value-a", "b": "value-b", "short": "ab"}}
	dir := doctorProject(t, "  A: fake://a\n  B: fake://missing,fake://b\n  S: fake://short\n")
	code, out, stderr := run(t, nil, cli.Deps{Providers: []mamori.Provider{fake}}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Contains(t, out, "ok   config: "+filepath.Join(dir, "tamesu.yaml")+" (version 1)\n")
	assert.Contains(t, out, "ok   suites: 1 file, 1 test\n")
	assert.Contains(t, out, "ok   secret A: fake (link 1 of 1)\n")
	assert.Contains(t, out, "ok   secret B: fake (link 2 of 2)\n")
	assert.Contains(t, out, "warn secret S: fake (link 1 of 1): resolved but too short to redact")
	assert.Contains(t, out, "skip models: not checked")
	assert.Contains(t, out, "skip chromium: not checked")
	assert.Contains(t, out, "skip livekit: not checked")
	assert.NotContains(t, out, "value-a")
}

func TestDoctorMissingSecretIsConfig(t *testing.T) {
	fake := &fakeProvider{values: map[string]string{}}
	dir := doctorProject(t, "  M: fake://nope\n")
	code, out, _ := run(t, nil, cli.Deps{Providers: []mamori.Provider{fake}}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, "fail secret M: secret M: not found in any of 1 ref(s): fake://nope\n")
}

func TestDoctorProviderDownIsInfra(t *testing.T) {
	fake := &fakeProvider{errs: map[string]error{"down": fmt.Errorf("fake: down: %w", mamori.ErrUnavailable)}}
	dir := doctorProject(t, "  D: fake://down\n")
	code, out, _ := run(t, nil, cli.Deps{Providers: []mamori.Provider{fake}}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitInfra, code)
	assert.Contains(t, out, "fail secret D: secret D: provider fake failed (unavailable)")
}

func TestDoctorRunsInjectedChecks(t *testing.T) {
	check := func(_ context.Context, env cli.DoctorEnv) []cli.DoctorResult {
		return []cli.DoctorResult{{Name: "models", Status: cli.StatusOK, Detail: fmt.Sprintf("%d secrets available", len(env.Secrets.Names()))}}
	}
	fake := &fakeProvider{values: map[string]string{"a": "value-a"}}
	dir := doctorProject(t, "  A: fake://a\n")
	code, out, _ := run(t, nil, cli.Deps{Providers: []mamori.Provider{fake}, Checks: []cli.DoctorCheck{check}}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code)
	assert.Contains(t, out, "ok   models: 1 secrets available\n")
	assert.NotContains(t, out, "skip models")
	assert.Contains(t, out, "skip chromium")
}

func TestDoctorUnsetEnvIsAnError(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\n",
		"s/x.yaml": `suite: env
targets:
  api: { type: rest, url: "${AGENT_URL}", response: { text: $.reply } }
tests:
  - { name: t, target: api, scenario: { goal: g } }
`,
	})
	code, out, _ := run(t, nil, cli.Deps{}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, "env var AGENT_URL is not set")
	code, _, _ = run(t, map[string]string{"AGENT_URL": "http://x"}, cli.Deps{}, "doctor", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code)
}
