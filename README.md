<div align="center">

<img src="site/public/favicon.svg" width="56" height="56" alt="tamesu: a seal with a waveform that resolves into a check" />

# 試す &nbsp;tamesu

### Put your web apps and AI agents to the test.

*An open-source, agentic testing framework and CLI written in Go: write tests in YAML or Go and
run them against web apps and AI agents (REST, realtime voice, phone) with one runner, one
assertion model and one report format.*

[![Go Reference](https://pkg.go.dev/badge/github.com/xavidop/tamesu.svg)](https://pkg.go.dev/github.com/xavidop/tamesu)
[![CI](https://github.com/xavidop/tamesu/actions/workflows/ci.yml/badge.svg)](https://github.com/xavidop/tamesu/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

---

`tamesu` (試す, Japanese for *to try / to put to the test*) describes a test the way you would
brief a colleague: a persona, a goal, and what a good outcome looks like. A simulated user then
talks to your agent or drives your web app, and deterministic checks and LLM judges grade the
result, with the same YAML, assertions and reports for a REST chatbot, a LiveKit voice agent, a
phone IVR and a web checkout.

<p align="center">
  <img src="site/public/demo.gif" alt="tamesu init, validate, list and doctor in a terminal" width="900" />
</p>

## Why

Agents are tested by hand: someone types a few messages, listens to a call, clicks through a
flow, and calls it good. The behaviour that matters (does it refund when it should not, does it
answer within two seconds, does it stop talking when interrupted) is never pinned down, so it
regresses silently.

tamesu turns those checks into a suite you run in CI. A **persona** with a **scenario** plays the
user; the **target** is whatever your agent runs on; **assertions** mix deterministic checks
(`contains`, `regex`, `tool_called`, `json_path`, `metric`) with structured **LLM judges**; and
every run leaves a transcript, metrics and a JUnit report behind.

- **One binary, four targets.** `browser`, `rest`, `livekit` (WebRTC) and `sip` (real phone
  calls through a LiveKit SIP service) share one schema, one assertion model and one report.
- **Framework agnostic.** Voice tests join a LiveKit room as a simulated caller, whatever the agent
  was built with (LiveKit Agents, Voiceflow, Pipecat, a Twilio IVR).
- **All AI through Genkit.** The simulated user, the judges and speech go through
  [Genkit Go](https://genkit.dev); switching provider is a one-line config change, and every run is
  traceable in the Genkit Developer UI.
- **Secrets through mamori.** API keys and tokens are declared as
  [mamori](https://github.com/xavidop/mamori) refs (`env:`, `aws-sm://`, `vault://`, `op://`, ...),
  resolved once per run, revealed only at the moment of use and redacted everywhere else.

## Install

- **Homebrew (macOS):** `brew install xavidop/tap/tamesu`. This is a Homebrew cask, so it is macOS only.
- **Scoop (Windows):** `scoop bucket add xavidop https://github.com/xavidop/scoop-bucket`, then
  `scoop install tamesu`.
- **Linux packages:** a `.deb`, `.rpm` or `.apk` for amd64 or arm64 from
  [GitHub Releases](https://github.com/xavidop/tamesu/releases), installed with `dpkg -i`,
  `rpm -i` or `apk add --allow-untrusted`. They include the shell completions.
- **Release binaries (Linux, macOS, Windows):** download an archive for amd64 or arm64 from
  [GitHub Releases](https://github.com/xavidop/tamesu/releases), check it against
  `checksums.txt`, unpack it and put `tamesu` on your `PATH`. `tamesu_*` is the full build (audio
  mode and SIP need CGO); `tamesu-lite_*` is pure Go and fully static.
- **Docker:** `ghcr.io/xavidop/tamesu` ships the CLI with Chromium preinstalled, for CI.
- **Go:** `go install github.com/xavidop/tamesu/cmd/tamesu@latest` builds the CLI from source
  (Go 1.27 or newer).

After you install, run `tamesu doctor` to check your setup. More options are on the
[install page](https://xavidop.github.io/tamesu/docs/install/).

## Quick start

```bash
tamesu init              # scaffold tamesu.yaml + examples/
tamesu validate          # schema-validate the suites, with file:line errors
tamesu doctor            # resolve every secret through mamori and check the setup
tamesu run               # run the suites: transcripts, metrics, junit.xml, exit code
```

`tamesu init` writes a `tamesu.yaml` and `examples/support-agent.yaml`, a suite with a REST
target and a LiveKit target:

```yaml
suite: support-agent
secrets:
  API_TOKEN: env:SUPPORT_API_TOKEN          # a mamori ref, never a value
targets:
  api:
    type: rest
    url: https://api.example.com/v1/chat
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request: { body: { session_id: "{{session}}", message: "{{message}}" } }
    response: { text: $.reply, tool_calls: $.tool_calls[*].name }
tests:
  - name: refund policy question
    target: api
    persona: { name: Marta, style: polite, slightly confused }
    scenario: { goal: "Find out if you can return shoes bought 40 days ago", max_turns: 8 }
    assert:
      - judge: "The agent states the 30-day return policy and does not promise an exception"
      - not_contains: "I'm just an AI"
      - metric: { name: turn_latency_p95_ms, lt: 4000 }
```

The commands:

```text
tamesu init [dir]                scaffold tamesu.yaml and examples/ (--force overwrites)
tamesu validate [paths...]       schema-validate suites without running them (--strict)
tamesu list [paths...]           list tests (--json, --filter, --tags, --target)
tamesu run [paths...]            run suites (--filter, --tags, --target, --parallel, --repeat, --mode, --out, --reporter, --max-cost-usd)
tamesu doctor [paths...]         check config, suites and secrets (via mamori), models, speech, Chromium, LiveKit and SIP
tamesu sip setup                 create or reuse the LiveKit SIP trunks and dispatch rule (--outbound, --inbound)
tamesu version                   print the version, commit and build date
```

Every command takes `--config`, `--verbose`, `--var KEY=VAL` and `--env-file`. `tamesu run`
exits 0 when every test passed, 1 when one failed, 2 on a config error, 3 on an infrastructure
error and 4 when the cost budget was exceeded. Planned, not in the CLI yet: `tamesu report`
(the HTML report), `tamesu record` (planned browser recorder) and `tamesu generate` (planned
scenario generator); see the
[roadmap](https://xavidop.github.io/tamesu/docs/roadmap/).

## Features

- **Suites in YAML, with a schema.** Targets, personas, scenarios, steps and assertions in one
  file; `schema/tamesu.schema.json` gives editors autocompletion, and `tamesu validate` reports
  every problem with its file and line.
- **One assertion model.** `judge` (an LLM rubric with structured output), `contains`,
  `not_contains`, `regex`, `json_path`, `status`, `tool_called`, `tool_not_called`,
  `dtmf_received`, `call_ended_by`, `transferred_to`, `evaluator` (any Genkit evaluator),
  `metric`, `turns` and `custom`, on the whole test or on one turn with `on_turn`.
- **Personas and scenarios.** Identity, style, language and voice; interruptions, silence and
  background noise for voice; a goal, a turn budget and a stop condition.
- **Secrets done right.** Declared as mamori refs with precedence chains (`env:X,aws-sm://...`),
  resolved once per run, held as `secret.String`, revealed only at the moment of use, and
  replaced by `‹secret:NAME›` in transcripts, logs and reports. `${VAR}` is for non-secret values
  and `tamesu validate` warns when it is used for a key.
- **`tamesu doctor`.** Validates the config and the suites, resolves every declared secret and
  reports which link of each chain answered, without printing a value.
- **A runner built for CI.** Repeats with a `pass_threshold`, retries on infrastructure errors,
  timeouts, parallel runs, a cost budget that stops the run (exit 4), and a run directory with
  `transcript.json` and `result.json` per run plus `summary.json` and `junit.xml`.
- **All AI through Genkit.** The simulated user and the judges are Genkit flows with dotprompt
  files you can override; every model is a Genkit reference; usage is priced on every call; a
  fake provider (`-tags llmfake`) runs the whole pipeline in CI without keys.
- **Determinism.** Seeds per test, versioned judge prompts recorded in every result, and metrics
  from transport timestamps, never from a model.

## Targets

| Target | Transport | Modes | Metrics | Tested against |
|---|---|---|---|---|
| `browser` | Chromium over CDP (chromedp) | agentic `goal` and `expect` steps, deterministic actions | `goal_steps`, `llm_cost_usd` | a bundled demo web app (runner planned) |
| `rest` | HTTP, SSE | text | `turn_latency_ms`, `time_to_first_token_ms`, `turns` | [`examples/rest-agent`](examples/rest-agent/), a Genkit agent, in CI with the fake provider |
| `livekit` | WebRTC room (LiveKit) | text and audio (TTS in, VAD and STT out, interruptions, noise, stereo WAV) | `turn_latency_ms`, `agent_join_ms`; audio adds `time_to_first_audio_ms`, `interruption_stop_ms`, `talk_over_ratio`, `silence_gap_max_ms` | [`examples/livekit-agent`](examples/livekit-agent/) on `livekit-server --dev`, in CI with the fake provider and fake speech models |
| `sip` | phone call through a LiveKit SIP service and your trunk (outbound or inbound, DTMF, `wait_for`/`say` scripts, line simulation) | audio | the audio metrics plus `call_setup_ms`, `ring_ms`, `greeting_ms`, `call_duration_ms`; `dtmf_received`, `call_ended_by`, `transferred_to` | [`examples/sip-ivr`](examples/sip-ivr/) through Asterisk as the carrier and IVR (`examples/compose.yaml`) |

All four target types are part of the suite schema, so suites can be written, validated and
listed; `rest`, `livekit` (text and audio) and `sip` run today, and browser lands next (see the
[roadmap](https://xavidop.github.io/tamesu/docs/roadmap/)).

## CI

tamesu is built for pipelines: deterministic exit codes (0 passed, 1 failed, 2 config or schema
error, 3 infrastructure error, 4 budget exceeded), file:line errors, JUnit output, and secrets that
come from your CI's secret store through mamori rather than from the environment.

```yaml
- uses: actions/checkout@v7
- run: go install github.com/xavidop/tamesu/cmd/tamesu@latest
- run: tamesu validate --strict
- run: tamesu doctor --var ENV=ci
```

The [CI page](https://xavidop.github.io/tamesu/docs/ci/) has GitHub Actions and Docker examples.

## Go API

The root module `github.com/xavidop/tamesu` has no heavy dependencies and is the public API:

```go
func TestRefundPolicy(t *testing.T) {
    client, _ := llm.New(ctx, llm.Config{Plugins: []string{"googlegenai"}, Models: llm.Models{Default: "googleai/gemini-2.5-flash"}})
    api := rest.New("api", rest.Config{URL: "https://api.example.com/v1/chat", TextPath: "$.reply"})
    tamesu.Run(t, tamesu.Test{
        Name:     "refund policy question",
        Target:   api,
        Persona:  tamesu.Persona{Name: "Marta", Style: "polite, slightly confused"},
        Scenario: tamesu.Scenario{Goal: "Find out if you can return shoes bought 40 days ago", MaxTurns: 8},
        Assert: []assert.Assertion{
            llm.Judge("The agent states the 30-day return policy and does not promise an exception"),
            assert.NotContains("I'm just an AI"),
            assert.Metric("turn_latency_ms_p95").Lt(4000),
        },
    }, tamesu.WithProvider(client))
}
```

`tamesu.Run` makes a subtest per repeat, logs the transcript and writes the run directory to
`TAMESU_OUT` or the test's temp dir. `tamesu.LoadSuite` loads a YAML suite, and `target`,
`assert` and `metrics` hold the contracts a target or an assertion implements. `llm.WithGenkit`
lets a Genkit app reuse its own instance to test its agent in-process.

## Agent skill

Teach your AI coding agent (Claude Code, Cursor, Copilot and others) to write and run tamesu
suites with the [agent skill](skills/tamesu/). It covers every command, assertion and target.

```bash
npx skills add xavidop/tamesu
```

## Documentation

- [Docs site](https://xavidop.github.io/tamesu/docs/): quick start, concepts, configuration
- [Install](https://xavidop.github.io/tamesu/docs/install/)
- [Suites](https://xavidop.github.io/tamesu/docs/suites/): the YAML reference, generated from the schema
- [CLI reference](https://xavidop.github.io/tamesu/docs/cli/): every command, flag and exit code
- [Security](https://xavidop.github.io/tamesu/docs/security/) and [SECURITY.md](SECURITY.md)
- [Go API reference](https://pkg.go.dev/github.com/xavidop/tamesu)

## Contributing

Contributions are welcome, especially new targets. Run `make all` before you send a change. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the repository layout, how to build and test, and the
commit conventions.

## License

[MIT](LICENSE) © Xavier Portilla Edo
