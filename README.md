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
tamesu list              # list the tests
tamesu doctor            # resolve every secret through mamori and check the setup
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

The commands the CLI has today:

```text
tamesu init [dir]                scaffold tamesu.yaml and examples/ (--force overwrites)
tamesu validate [paths...]       schema-validate suites without running them (--strict)
tamesu list [paths...]           list tests (--json, --filter, --tags, --target)
tamesu doctor [paths...]         check config, suites and secrets (via mamori), models, Chromium and LiveKit
tamesu version                   print the version, commit and build date
```

Every command takes `--config`, `--verbose`, `--var KEY=VAL` and `--env-file`.
Planned, not in the CLI yet: `tamesu run` (the runner), `tamesu report` (the HTML report),
`tamesu record` (planned browser recorder), `tamesu generate` (planned scenario generator) and
`tamesu sip setup` (planned); see the [roadmap](https://xavidop.github.io/tamesu/docs/roadmap/).

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
- **Determinism and budgets.** Seeds per test, versioned judge prompts, cost and token budgets,
  deterministic metrics from transport timestamps; repeats with a `pass_threshold` for variance.

## Targets

| Target | Transport | Modes | Metrics | Tested against |
|---|---|---|---|---|
| `browser` | Chromium over CDP (chromedp) | agentic `goal` and `expect` steps, deterministic actions | `goal_steps`, `llm_cost_usd` | a bundled demo web app (runner planned) |
| `rest` | HTTP, SSE, WebSocket | text | `turn_latency_ms`, `time_to_first_token_ms`, `turns` | an example Genkit agent (runner planned) |
| `livekit` | WebRTC room (LiveKit) | text, audio | `turn_latency_ms`, `time_to_first_audio_ms`, `interruption_stop_ms`, `talk_over_ratio` | `livekit-server --dev` (runner planned) |
| `sip` | phone call through a LiveKit SIP service and your trunk | audio | `call_setup_ms`, `ring_ms`, `greeting_ms`, `call_duration_ms` | Asterisk as the carrier (runner planned) |

All four target types are part of the suite schema today, so suites can be written, validated and
listed; the runners land phase by phase (see the
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
suite, err := tamesu.LoadSuite("tests/support.yaml")   // loads and validates a YAML suite
```

`target`, `assert` and `metrics` hold the contracts a target or an assertion implements. Running
tests from `go test` with `tamesu.Run` is planned with the runner.

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
