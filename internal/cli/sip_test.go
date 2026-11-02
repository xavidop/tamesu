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
	"github.com/xavidop/tamesu/internal/model"
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

// generatingModel is a fake provider that also drafts tests.
type generatingModel struct {
	fakeModel
	req model.GenerateRequest
}

func (g *generatingModel) GenerateTests(_ context.Context, req model.GenerateRequest) (model.Draft, error) {
	g.req = req
	if req.Type == "browser" {
		return model.Draft{Tests: []model.DraftTest{{Name: "log in", Steps: []string{"Log in as {{vars.user}} with the password {{secrets.TEST_PASSWORD}}"}, Expects: []string{"The user is signed in"}}}}, nil
	}
	return model.Draft{Tests: []model.DraftTest{
		{Name: "refund inside the window", Tags: []string{"returns"}, Persona: "Marta", Style: "polite", Goal: "Return shoes bought 10 days ago", MaxTurns: 6, Judges: []string{"The agent accepts the return"}},
		{Name: "refund after the window", Persona: "Jordi", Goal: "Insist on a refund after 40 days", Judges: []string{"The agent refuses and offers the voucher"}, NotContains: []string{"I'm just an AI"}},
	}}, nil
}

func TestGenerateCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("version: 1\nmodels: { default: fake/text, scenario_generator: fake/generate }\n"), 0o644))
	prd := filepath.Join(dir, "PRD.md")
	require.NoError(t, os.WriteFile(prd, []byte("# Support agent\n\nReturns within 30 days.\n"), 0o644))
	gen := &generatingModel{}
	deps := cli.Deps{
		LookupEnv:   func(string) (string, bool) { return "", false },
		NewProvider: func(context.Context, cli.ProviderConfig) (model.Provider, error) { return gen, nil },
	}
	out := filepath.Join(dir, "suites", "generated.yaml")
	var stdout, stderr bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	code := cli.Execute(context.Background(), root, []string{"generate", "--config", cfg, "--from", prd, "--target", "api", "--count", "2", "--out", out})
	assert.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "api", gen.req.Target)
	assert.Equal(t, "rest", gen.req.Type)
	assert.Equal(t, 2, gen.req.Count)
	assert.Contains(t, gen.req.Source, "Returns within 30 days")
	assert.Contains(t, stdout.String(), "2 draft test(s) for target api (rest)")
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), "suite: prd\n")
	assert.Contains(t, string(data), "- judge: The agent refuses and offers the voucher")

	// The draft validates with tamesu validate.
	stdout.Reset()
	stderr.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"validate", "--config", cfg, out}), stderr.String())

	// A browser draft, printed.
	stdout.Reset()
	stderr.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"generate", "--config", cfg, "--from", prd, "--target", "web", "--type", "browser"}), stderr.String())
	assert.Contains(t, stdout.String(), "type: browser")
	assert.Contains(t, stdout.String(), "- expect: The user is signed in")

	// Usage errors.
	stderr.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"generate", "--config", cfg, "--from", prd}))
	assert.Contains(t, stderr.String(), "--target <name> are required")
	stderr.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"generate", "--config", cfg, "--from", prd, "--target", "x", "--type", "fax"}))
	assert.Contains(t, stderr.String(), "--type must be one of")
}

func TestRecordBrowserCommand(t *testing.T) {
	dir := t.TempDir()
	deps := cli.Deps{RecordBrowser: func(_ context.Context, req cli.RecordRequest) (cli.RecordResult, error) {
		steps := []map[string]any{{"navigate": req.URL}, {"click": "#add"}}
		if req.Log != nil {
			for _, s := range steps {
				req.Log(s)
			}
		}
		return cli.RecordResult{Steps: steps, YAML: "steps:\n  - action: { navigate: \"" + req.URL + "\" }\n  - action: { click: \"#add\" }\n"}, nil
	}}
	var stdout, stderr bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"record", "browser", "http://shop.test/"}))
	assert.Contains(t, stdout.String(), `- action: { click: "#add" }`)
	assert.Contains(t, stderr.String(), "recorded click: #add")

	out := filepath.Join(dir, "steps.yaml")
	stdout.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, deps)
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"record", "browser", "http://shop.test/", "--out", out}))
	assert.Contains(t, stdout.String(), "steps.yaml: 2 step(s)")
	_, err := os.Stat(out)
	assert.NoError(t, err)

	stderr.Reset()
	root = cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &stdout, Err: &stderr}, cli.Deps{})
	assert.Equal(t, cli.ExitConfig, cli.Execute(context.Background(), root, []string{"record", "browser", "http://shop.test/"}))
	assert.Contains(t, stderr.String(), "not available in this build")
}
