<div align="center">

<img src="site/public/favicon.svg" width="56" height="56" alt="tamesu: a seal with a waveform that resolves into a check" />

# 試す &nbsp;tamesu

### Your agents, tested like users would.

*An open-source agentic testing framework and CLI in Go: describe a test in YAML, a simulated
user talks to your agent or drives your web app, and deterministic checks and LLM judges grade
the result.*

[![Go Reference](https://pkg.go.dev/badge/github.com/xavidop/tamesu.svg)](https://pkg.go.dev/github.com/xavidop/tamesu)
[![CI](https://github.com/xavidop/tamesu/actions/workflows/ci.yml/badge.svg)](https://github.com/xavidop/tamesu/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

---

Agents are usually tested by hand: someone types a few messages, listens to a call, clicks
through a flow, and calls it good. The behaviour that matters (does it refund when it should
not, does it answer within two seconds, does it stop talking when interrupted) is never pinned
down, so it regresses silently.

`tamesu` (試す, Japanese for *to try, to test*) turns those checks into a suite you run in CI.
A **persona** with a **scenario** plays the user, the **target** is whatever your agent runs on,
and **assertions** mix deterministic checks with structured **LLM judges**. The same YAML,
assertions and reports cover a REST chatbot, a LiveKit or Pipecat voice agent, a phone IVR, the
OpenAI Realtime API and a web checkout.

<p align="center">
  <img src="site/public/demo.gif" alt="tamesu init, validate, list and doctor in a terminal" width="900" />
</p>

- **Seven targets, one model.** `browser`, `rest`, `grpc`, `livekit`, `sip`, `realtime` and
  `pipecat` share the schema, the assertions, the metrics and the reports.
- **Judges you can trust.** Rubrics with structured output, versioned prompts recorded in every
  result, replayable verdicts, and `tamesu judge calibrate` to measure a judge against
  transcripts people have already marked.
- **Real conversations as tests.** `tamesu import` replays recorded conversations against the
  agent; `tamesu generate` drafts tests from a PRD or system prompt; red-team packs cover prompt
  injection and leaks.
- **Built for CI.** Repeats with a pass threshold, retries, budgets, sharding, baselines with
  regression limits, quarantine for known flakes, and JUnit, HTML, Markdown, GitHub and Slack
  reports.
- **Secrets through [mamori](https://github.com/xavidop/mamori), AI through
  [Genkit](https://genkit.dev).** Keys are refs (`env:`, `aws-sm://`, `vault://`, `op://`),
  revealed only at the moment of use and redacted everywhere else; models are a one-line config
  change.

## Install

- **macOS:** `brew install xavidop/tap/tamesu` (a Homebrew cask, so macOS only).
- **Windows (Scoop):** `scoop bucket add xavidop https://github.com/xavidop/scoop-bucket`, then
  `scoop install tamesu`.
- **Linux:** a `.deb`, `.rpm` or `.apk` for amd64 or arm64 from
  [GitHub Releases](https://github.com/xavidop/tamesu/releases); the archives there also cover
  macOS and Windows. `tamesu_*` is the full build (audio mode and SIP), `tamesu-lite_*` is pure Go.
- **Docker:** `ghcr.io/xavidop/tamesu`, with Chromium preinstalled for CI.
- **Go:** `go install github.com/xavidop/tamesu/cmd/tamesu@latest` (Go 1.27 or newer).

Then run `tamesu doctor`. Details on the [install page](https://xavidop.github.io/tamesu/docs/install/).

## Quick start

```bash
tamesu init              # scaffold tamesu.yaml + examples/
tamesu validate          # schema-validate the suites, with file:line errors
tamesu doctor            # resolve every secret and check models, speech, Chromium, LiveKit
tamesu run               # run the suites: transcripts, metrics, junit.xml, exit code
```

A test reads the way you would brief a colleague:

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
tamesu init [dir]                scaffold tamesu.yaml and examples/ (--force)
tamesu validate [paths...]       schema-validate suites (--strict)
tamesu list [paths...]           list tests (--json, --filter, --tags, --target, --shard)
tamesu run [paths...]            run suites (--filter, --tags, --target, --shard, --fail-fast, --parallel, --repeat, --load, --mode, --replay, --baseline, --label, --out, --reporter)
tamesu doctor [paths...]         check config, suites, secrets, models, speech, Chromium, LiveKit and SIP
tamesu report <run-dir>          rebuild report.html from a run directory
tamesu baseline save <run-dir>   keep a run as the baseline (--dir)
tamesu compare <base> <run>      compare a run with a baseline; exit 1 on a regression
tamesu record browser <url>      record a browser session into deterministic steps (--out)
tamesu generate                  draft tests from a PRD or system prompt (--from, --target, --type, --count, --out)
tamesu import <file>             turn recorded conversations into a suite that replays them (--target, --out, --golden-dir)
tamesu judge calibrate <labels>  measure the judge against labelled transcripts (--min-agreement)
tamesu sip setup                 create or reuse the LiveKit SIP trunks and dispatch rule (--outbound, --inbound)
tamesu mcp                       serve tamesu to coding agents over MCP, stdio or HTTP (--cwd, --http)
tamesu version                   print the version, commit and build date
```

`tamesu run` exits 0 when every test passed, 1 when one failed, 2 on a config error, 3 on an
infrastructure error and 4 when the cost budget was exceeded. The [CI page](https://xavidop.github.io/tamesu/docs/ci/)
has GitHub Actions, GitLab and Docker examples; the [Go API](https://xavidop.github.io/tamesu/docs/go-api/)
runs the same tests from `go test`.

## Targets

| Target | What it talks to | Modes | Example |
|---|---|---|---|
| `browser` | Chromium over CDP: agentic `goal` and `expect` steps, deterministic actions, request mocks, an axe-core audit, visual baselines, a recording and a HAR per run | — | [`examples/browser-shop`](examples/browser-shop/) against [`examples/webapp`](examples/webapp/) |
| `rest` | HTTP, SSE or WebSocket | text | [`examples/rest-agent`](examples/rest-agent/) |
| `livekit` | a LiveKit room over WebRTC, whatever the agent is built with | text, audio | [`examples/livekit-agent`](examples/livekit-agent/) |
| `sip` | a phone call through a LiveKit SIP service and your trunk: outbound or inbound, DTMF, line simulation | audio | [`examples/sip-ivr`](examples/sip-ivr/) with Asterisk |
| `realtime` | the OpenAI Realtime protocol: the API, or your server speaking its events | text, audio | your `session_update` and tools |
| `grpc` | the turn API in [`proto/tamesu/turn/v1`](proto/tamesu/turn/v1/turn.proto) | text | [`examples/rest-agent`](examples/rest-agent/) with `--grpc` |
| `pipecat` | the websocket transport of a Pipecat bot (protobuf frames and RTVI) | text, audio | any bot with an RTVI observer |

Audio tests speak through TTS, hear through VAD and STT, interrupt, add background noise and
degrade the line, and measure time to first audio, interruption stop time, talk-over and silence
gaps from the transport, never from a model. Each target has its own page in the docs, from
[browser](https://xavidop.github.io/tamesu/docs/targets/browser/) to
[pipecat](https://xavidop.github.io/tamesu/docs/targets/pipecat/).

## Agent skill

Teach your coding agent (Claude Code, Cursor, Copilot and others) to write and run tamesu suites
with the [agent skill](skills/tamesu/), or give it the tools: `tamesu mcp` is an MCP server with
the schema, validate, list, run, generate and doctor as tools and the run directories as
resources ([MCP server](https://xavidop.github.io/tamesu/docs/mcp/)).

```bash
npx skills add xavidop/tamesu
claude mcp add tamesu -- tamesu mcp --cwd /path/to/project
```

## Documentation

- [Docs](https://xavidop.github.io/tamesu/docs/): quick start, concepts, configuration
- [Recipes](https://xavidop.github.io/tamesu/docs/recipes/): complete projects to start from
- [Suites](https://xavidop.github.io/tamesu/docs/suites/): the YAML reference, generated from the schema
- [Assertions](https://xavidop.github.io/tamesu/docs/assertions/) and [judges](https://xavidop.github.io/tamesu/docs/judges/)
- [CLI reference](https://xavidop.github.io/tamesu/docs/cli/): every command, flag and exit code
- [Security](https://xavidop.github.io/tamesu/docs/security/) and [SECURITY.md](SECURITY.md)
- [Go API reference](https://pkg.go.dev/github.com/xavidop/tamesu)

## Contributing

Contributions are welcome, especially new targets. Run `make all` before you send a change; see
[CONTRIBUTING.md](CONTRIBUTING.md) for the layout, the build and the commit conventions.

## License

[MIT](LICENSE) © Xavier Portilla Edo
