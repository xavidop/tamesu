package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestSIPSetup(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`version: 1
sip:
  livekit_url: ws://lk:7880
  inbound_number: "+34910000000"
  trunk:
    address: sip.twilio.com
    numbers: ["+34910000000"]
    auth_username: "{{secrets.SIP_USER}}"
    auth_password: "{{secrets.SIP_PASSWORD}}"
secrets:
  LIVEKIT_API_KEY: env:LK_KEY
  LIVEKIT_API_SECRET: env:LK_SECRET
  SIP_USER: env:SIP_USER
  SIP_PASSWORD: env:SIP_PASSWORD
`), 0o644))
	// env: refs are resolved by mamori from the process environment.
	env := map[string]string{"LK_KEY": "devkey", "LK_SECRET": "secretsecret", "SIP_USER": "trunk-user", "SIP_PASSWORD": "trunk-pass-123"}
	for k, v := range env {
		t.Setenv(k, v)
	}
	var got cli.SIPSetupRequest
	deps := cli.Deps{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		SIPSetup: func(_ context.Context, req cli.SIPSetupRequest) (cli.SIPSetupResult, error) {
			got = req
			return cli.SIPSetupResult{OutboundTrunkID: "ST_out", InboundTrunkID: "ST_in", DispatchRuleID: "SDR_1", Created: []string{"outbound trunk ST_out"}, Reused: []string{"inbound trunk ST_in", "dispatch rule SDR_1"}, YAML: "sip:\n  outbound_trunk_id: ST_out\n  inbound_trunk_id: ST_in\n  dispatch_rule_id: SDR_1\n"}, nil
		},
	}
	var out, errOut bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{Version: "test"}, cli.Streams{Out: &out, Err: &errOut}, deps)
	code := cli.Execute(context.Background(), root, []string{"sip", "setup", "--config", cfg, "--inbound"})
	assert.Equal(t, 0, code, errOut.String())
	assert.Equal(t, "ws://lk:7880", got.LiveKitURL)
	assert.Equal(t, "devkey", got.APIKey)
	assert.Equal(t, "secretsecret", got.APISecret)
	assert.True(t, got.Inbound)
	assert.False(t, got.Outbound)
	user, err := got.Render(got.Project.SIP.Trunk.AuthUsername)
	require.NoError(t, err)
	assert.Equal(t, "trunk-user", user, "the trunk's auth secrets render")
	assert.Contains(t, out.String(), "created outbound trunk ST_out")
	assert.Contains(t, out.String(), "reused  dispatch rule SDR_1")
	assert.Contains(t, out.String(), "outbound_trunk_id: ST_out")
	assert.NotContains(t, out.String(), "trunk-pass-123", "secrets never reach the output")

	// Failures: no trunk, no keys, a failing API, a build without sip.
	require.NoError(t, os.WriteFile(cfg, []byte("version: 1\nsip:\n  livekit_url: ws://lk:7880\n"), 0o644))
	out.Reset()
	errOut.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, deps)
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"sip", "setup", "--config", cfg}))
	assert.Contains(t, errOut.String(), "sip.trunk is not set")

	require.NoError(t, os.WriteFile(cfg, []byte("version: 1\nsip:\n  livekit_url: ws://lk:7880\n  trunk: { address: pbx, numbers: [\"+34910000000\"] }\n"), 0o644))
	errOut.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }, SIPSetup: deps.SIPSetup})
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"sip", "setup", "--config", cfg}))
	assert.Contains(t, errOut.String(), "LIVEKIT_API_KEY and LIVEKIT_API_SECRET")

	errOut.Reset()
	failing := deps
	failing.SIPSetup = func(context.Context, cli.SIPSetupRequest) (cli.SIPSetupResult, error) {
		return cli.SIPSetupResult{}, errors.New("twirp: unauthenticated")
	}
	env["LIVEKIT_API_KEY"], env["LIVEKIT_API_SECRET"] = "k", "s"
	t.Setenv("LIVEKIT_API_KEY", "k")
	t.Setenv("LIVEKIT_API_SECRET", "s")
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, failing)
	assert.Equal(t, cli.ExitInfra, cli.Execute(context.Background(), root, []string{"sip", "setup", "--config", cfg}))
	assert.Contains(t, errOut.String(), "unauthenticated")

	errOut.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{})
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"sip", "setup", "--config", cfg}))
	assert.Contains(t, errOut.String(), "not available in this build")
}

func TestRunRefusesDisallowedNumbers(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("version: 1\nsuites: [\"suite.yaml\"]\nsip:\n  livekit_url: ws://lk:7880\n  outbound_trunk_id: ST_1\n  allowed_numbers: [\"+1555000*\"]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(`suite: phone
vars: { agent_number: "+15550001" }
targets:
  stranger: { type: sip, direction: outbound, to: "{{vars.agent_number}}", from: "+15550002" }
tests:
  - name: dial
    target: stranger
    persona: { name: Nobody }
    scenario: { goal: "nothing", max_turns: 1 }
    assert:
      - call_ended_by: [caller]
`), 0o644))
	var out, errOut bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
	code := cli.Execute(context.Background(), root, []string{"run", "--config", cfg, "--var", "agent_number=+34910000000", "--out", filepath.Join(dir, "out")})
	assert.Equal(t, cli.ExitConfig, code, errOut.String())
	assert.Contains(t, errOut.String(), "would dial +34910000000, which is not in sip.allowed_numbers")
}

func TestReportCommand(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "summary.json"), []byte(`{"started_at":"2026-11-02T10:00:00Z","finished_at":"2026-11-02T10:00:05Z","out_dir":"","tests":[{"suite":"s","name":"t","target":"api","type":"rest","repeat":1,"pass_threshold":1,"passed":true,"runs":[{"index":1,"passed":true,"assertions":[],"metrics":{"turns":2},"transcript":[{"role":"user","text":"hi"},{"role":"agent","text":"hello","index":1}],"dir":""}]}],"usage":{"calls":1,"input_tokens":10,"output_tokens":5,"cost_usd":0.001}}`), 0o644))
	var out, errOut bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{})
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"report", run}), errOut.String())
	assert.Contains(t, out.String(), "report.html: 1 passed, 0 failed, 0 errors")
	html, err := os.ReadFile(filepath.Join(run, "report.html"))
	require.NoError(t, err)
	assert.Contains(t, string(html), "hello")

	errOut.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{})
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"report", dir}))
	assert.Contains(t, errOut.String(), "has no summary.json")
}
