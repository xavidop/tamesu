# tamesu (試す) — Specification

> **tamesu** (試す, *"to try / to put to the test"*) is an open-source, agentic testing framework and CLI written in Go.
> Write tests in YAML or Go, and run them against **web apps** (agentic browser tests) and **AI agents** (REST and realtime voice over WebRTC/LiveKit) with one runner, one assertion model and one report format.

This document is the build spec for Claude Code. Follow it phase by phase. Each phase ends with acceptance criteria that must pass before moving on.

---

## 1. Goals and non-goals

### Goals
- **One binary**: `tamesu` is a single static Go binary. It needs no Python or Node runtime for core features.
- **Four target types sharing one model**: `browser`, `rest`, `livekit` (WebRTC) and `sip` (real phone calls through LiveKit SIP), with the same YAML schema, assertions, LLM judges, retries and reports.
- **Agent-framework agnostic**: voice tests join any LiveKit room as a simulated caller, whether the agent under test was built with LiveKit Agents, Voiceflow, Pipecat or anything else.
- **Two authoring modes**: declarative YAML for most users, and a Go API that runs inside `go test`.
- **CI first**: deterministic where possible, with JUnit/JSON output, proper exit codes, parallelism, and cost and latency budgets.
- **All AI through Genkit Go**: every model call goes through Genkit — the simulated user, the browser agent, the judges, scenario generation, and speech (STT/TTS) too. Nothing calls a model SDK directly. See section 7.1.

### Non-goals (v1)
- No hosted SaaS or dashboard. The HTML report is static.
- No PSTN carrier of our own: SIP calls go through a LiveKit SIP service and a trunk you bring (Twilio, Telnyx, Plivo, your own PBX…).
- No mobile app testing.
- No load testing beyond modest concurrency (`--parallel`).

---

## 2. Concepts

| Concept | Description |
|---|---|
| **Suite** | A YAML file (or Go package) containing targets, defaults and tests. |
| **Target** | The system under test: `browser`, `rest`, `livekit` or `sip`. |
| **Test** | One scenario run against one target. |
| **Step** | For browser tests: a natural-language `goal`, a deterministic `action`, or an `expect`. |
| **Persona** | The simulated user for agent tests: identity, goal, style, voice, behaviors (interrupts, silence, noise). |
| **Scenario** | What the persona is trying to achieve and when to stop. |
| **Assertion** | A check evaluated during or after a run: deterministic (`contains`, `regex`, `status`, `json_path`, `tool_called`, `metric`) or LLM-based (`judge`). |
| **Run** | One execution of a test. A test may run N times (`repeat`) to measure variance. |
| **Artifact** | Anything captured: transcript, audio (WAV), screenshots, trace, HAR, Genkit trace IDs. |

---

## 3. CLI

```
tamesu init                      # scaffold tamesu.yaml + examples/
tamesu run [paths...]            # run suites (globs allowed)
tamesu validate [paths...]       # schema-validate suites without running
tamesu list [paths...]           # list tests
tamesu record browser <url>      # (phase 6) record a browser session into YAML steps
tamesu report <run-dir>          # (re)build the HTML report from a run directory
tamesu generate --from <file> --target <name>   # draft tests from a PRD / system prompt / KB (Genkit flow)
tamesu sip setup                 # create or reuse LiveKit SIP trunks + dispatch rule (idempotent)
tamesu doctor                    # check config, suites, secrets (via mamori), models, Chromium and LiveKit reachability
tamesu version
```

### Common flags for `run`
```
--filter <regex>          run tests whose name matches
--tags a,b                run tests having any of these tags
--target <name>           restrict to a target
--parallel N              concurrent tests (default: GOMAXPROCS, voice tests capped by config)
--repeat N                override repeat count
--mode text|audio         voice tests: text-only simulation or real audio (default from suite)
--headed                  show the browser
--replay                  browser: use cached action plans only, fail if cache misses
--update-cache            browser: re-plan and refresh the action cache
--out <dir>               run output dir (default .tamesu/runs/<timestamp>)
--reporter console,junit,json,html
--max-cost-usd X          abort the run once the estimated LLM spend exceeds X
--env-file .env           loaded through mamori's dotenv provider
--var KEY=VAL             ref vars for mamori ${VAR} expansion (repeatable)
-v / --verbose
```

### Exit codes
| Code | Meaning |
|---|---|
| 0 | all tests passed |
| 1 | one or more tests failed |
| 2 | config / schema error |
| 3 | infrastructure error (browser couldn't start, LiveKit unreachable, model error after retries) |
| 4 | budget exceeded |

---

## 4. Configuration

`tamesu.yaml` holds project-level defaults. Suites can override any of them.

```yaml
version: 1
genkit:
  plugins: [googlegenai, anthropic, openai, ollama, deepgram, elevenlabs]   # only these are initialised
  dev_ui: false                             # true: export traces to the Genkit Developer UI (genkit start -- tamesu run …)
models:                                     # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-5
  browser_agent: googleai/gemini-2.5-pro
  simulator: openai/gpt-4.1-mini
  scenario_generator: googleai/gemini-2.5-pro
speech:                                     # voice audio mode and sip; still Genkit models (section 7.1)
  stt: { model: deepgram/nova-3, language: en }          # or googleai/gemini-2.5-flash (audio input)
  tts: { model: googleai/gemini-2.5-flash-preview-tts }  # or elevenlabs/eleven_flash_v2_5, openai/gpt-4o-mini-tts
  vad: { provider: silero }                 # onnxruntime-go (not a model call, so not Genkit)
sip:
  livekit_url: ${LIVEKIT_URL}               # LiveKit server with the SIP service enabled
  outbound_trunk_id: ST_xxxxxxxx            # created once with `tamesu sip setup` or the LiveKit CLI
  inbound_number: "+34910000000"            # number your trunk routes to tamesu (for testing outbound agents)
  max_concurrent_calls: 2
  allowed_numbers: ["+1800555*", "+3491*"]  # E.164 patterns tamesu may dial
defaults:
  timeout: 3m
  repeat: 1
  retries: 0
  pass_threshold: 1.0                       # fraction of repeats that must pass
budgets:
  max_cost_usd: 5
  max_tokens: 2_000_000
livekit:
  max_concurrent_rooms: 4
reporters: [console, junit, html]
secrets:                                    # resolved with mamori (section 4.1)
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ANTHROPIC_API_KEY: env:ANTHROPIC_API_KEY,aws-sm://ci/tamesu#anthropic
  DEEPGRAM_API_KEY: vault://secret/data/tamesu#deepgram
  ELEVENLABS_API_KEY: op://CI/elevenlabs/credential
```

Plain (non-secret) environment interpolation works everywhere: `${VAR}` and `${VAR:-default}`. Template helpers are `{{uuid}}`, `{{now}}`, `{{env.X}}`, `{{vars.x}}`, `{{secrets.X}}` and `{{faker.email}}`.

### 4.1 Secrets with mamori

All secrets go through [mamori](https://github.com/xavidop/mamori) (`github.com/xavidop/mamori`). tamesu never reads an API key, password or token any other way.

- **Declaring**: a `secrets:` map in `tamesu.yaml` and/or in a suite (suite entries override project entries by name). Each value is a mamori ref, including precedence chains (`env:X,aws-sm://…`), `#key` / JSON-Pointer selection, `?decode=base64`, and `${VAR}` expansion fed by `mamori.WithRefVars` (populated from `--var KEY=VAL` and the suite's `vars`, never from the ambient environment).
- **Resolving**: once per run, before any test starts, with `mamori.Resolve(ctx, ref, …)` for every secret actually referenced by the selected tests (unreferenced secrets are not fetched). A not-found at the end of a chain is a config error (exit 2); a provider failure is an infrastructure error (exit 3). Resolved values are held as mamori `secret.String` and only `Reveal()`ed at the moment of use (HTTP header, LiveKit token signing, Genkit plugin init, typing into a browser field).
- **Using**: `{{secrets.NAME}}` anywhere a string is accepted, e.g. `api_key: "{{secrets.LIVEKIT_API_KEY}}"` or `Authorization: "Bearer {{secrets.API_TOKEN}}"`. `${VAR}` is for non-secret values only; `tamesu validate` warns when `${VAR}` is used in a field documented as secret (`api_key`, `api_secret`, `headers.Authorization`, `password`, `token`).
- **Model credentials**: the `llm` module initialises Genkit provider plugins from the resolved secrets by conventional name (`GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, …), or from `models.credentials: { googleai: "{{secrets.X}}" }`.
- **Providers**: the CLI (`cmd/tamesu`) registers mamori's built-ins (`env:`, `file://`, dotenv) plus the cloud providers people use in CI: `aws-sm://`, `aws-ps://`, `vault://`, `gcp-sm://`, `azure-kv://`, `k8s-secret://`, `op://` (1Password), `doppler://`, `infisical://`, `sops://`. Each provider is its own mamori module, so the core tamesu module only depends on mamori's core (no cloud SDKs), and `check_root_deps.sh` enforces that.
- **`--env-file`** maps to mamori's dotenv provider rather than mutating the process environment.
- **Go API**: `tamesu.WithSecrets(map[string]string{"API_TOKEN": "env:API_TOKEN"})` and `tamesu.WithMamoriOptions(...)` (custom providers, ref vars, timeouts). Tests that embed tamesu can also pass already-loaded `secret.String` values.
- **`tamesu doctor`** (new command): validates config and suites, resolves every declared secret and reports which ones resolved, from which link of the chain, without printing values.

---

## 5. Suite schema (YAML)

A JSON Schema for this format is generated from the Go structs and published at `schema/tamesu.schema.json`, so editors can autocomplete it.

```yaml
suite: support-agent
tags: [smoke]
vars:
  user: qa@example.com
  ENV: staging
secrets:                                          # mamori refs, see 4.1
  API_TOKEN: env:SUPPORT_API_TOKEN,aws-sm://${ENV}/support-agent#api_token
  TEST_PASSWORD: aws-sm://${ENV}/qa-user#password
  LIVEKIT_API_KEY: env:LIVEKIT_API_KEY,vault://secret/data/livekit#api_key
  LIVEKIT_API_SECRET: env:LIVEKIT_API_SECRET,vault://secret/data/livekit#api_secret

targets:
  web:
    type: browser
    url: https://app.example.com
    browser: { engine: chromium, viewport: 1280x800, locale: en-US }
    auth: { storage_state: .auth/web.json }      # optional pre-auth

  api:
    type: rest
    url: https://api.example.com/v1/chat
    method: POST
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request:                                      # how a user turn is sent
      body: { session_id: "{{session}}", message: "{{message}}" }
    response:                                     # how the agent turn is read
      text: $.reply                               # JSONPath
      tool_calls: $.tool_calls[*].name
      done: $.end_session
    streaming: none                               # none | sse | websocket
    session: { id: "{{uuid}}" }

  voice:
    type: livekit
    url: ${LIVEKIT_URL}
    api_key: "{{secrets.LIVEKIT_API_KEY}}"
    api_secret: "{{secrets.LIVEKIT_API_SECRET}}"
    room: "support-{{uuid}}"
    dispatch: { agent_name: support-agent }       # optional explicit agent dispatch
    agent_identity_prefix: agent-                 # how to recognise the agent participant
    mode: text                                    # text | audio
    data_topic: lk.chat                           # text mode transport (data channel / text streams)

  phone:
    type: sip
    direction: outbound                           # outbound: tamesu calls the agent | inbound: the agent calls tamesu
    to: "+18005550100"                            # the agent's phone number (outbound)
    from: "+34910000000"                          # caller ID presented by the trunk
    trunk_id: ST_xxxxxxxx                         # defaults to sip.outbound_trunk_id
    ring_timeout: 30s
    max_call_duration: 5m
    # inbound only: how to make the agent call us, then we answer on sip.inbound_number
    # trigger: { http: { url: https://api.example.com/campaigns/test-call, method: POST,
    #            body: { to: "{{sip.inbound_number}}" }, headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" } } }

tests:
  - name: checkout happy path
    target: web
    steps:
      - goal: "Log in as {{vars.user}} with password {{secrets.TEST_PASSWORD}}"
      - goal: "Search for 'laptop' and add the first result to the cart"
      - expect: "The cart badge shows 1 item"
      - action: { screenshot: cart }

  - name: refund policy question
    target: api
    persona:
      name: Marta
      style: polite, slightly confused
      language: es
    scenario:
      goal: "Find out if you can return shoes bought 40 days ago"
      max_turns: 8
      stop_when: "the agent has given a clear yes/no answer"
    assert:
      - judge: "The agent states the 30-day return policy and does not promise an exception"
      - not_contains: "I'm just an AI"
      - metric: { name: turn_latency_p95_ms, lt: 4000 }

  - name: reschedule booking by voice
    target: voice
    mode: audio
    repeat: 5
    pass_threshold: 0.8
    persona:
      name: Alice
      voice: en-GB-female
      style: impatient
      behaviors:
        interrupt_probability: 0.3
        silence_after_turn_ms: [300, 1500]
        background_noise: cafe
    scenario:
      goal: "You booked a room for Friday; move it to Saturday"
      max_turns: 12
    assert:
      - judge: "The agent confirmed the new date and did not invent availability"
      - tool_called: { name: reschedule_booking, args: { day: saturday } }
      - metric: { name: time_to_first_audio_ms, lt: 1500 }
      - metric: { name: talk_over_ratio, lt: 0.1 }

  - name: phone IVR routes to billing
    target: phone
    persona:
      name: Jordi
      voice: es-ES-male
      style: calm
      behaviors: { background_noise: street, line_quality: pstn }   # 8 kHz G.711, packet loss/jitter profile
    scenario:
      goal: "Reach billing and ask why your last invoice is higher"
      max_turns: 10
      script:                                     # optional deterministic steps mixed with free conversation
        - wait_for: "press 2 for billing"         # matched on the STT transcript (fuzzy)
        - dtmf: "2"
        - say: "Hi, my last invoice is higher than usual"   # then the persona takes over
    assert:
      - judge: "The caller reached billing and got an explanation, or was offered a transfer to a human"
      - metric: { name: call_setup_ms, lt: 5000 }
      - metric: { name: time_to_first_audio_ms, lt: 2000 }
      - call_ended_by: [agent, caller]            # fail on dropped calls or ring timeouts
      - transferred_to: { matches: "^sip:billing@" }   # when the agent does a SIP REFER
```

### Browser step types
- `goal: <text>` — agentic: the browser agent plans and executes actions until the goal is met or the step budget runs out.
- `expect: <text>` — agentic assertion against the current page (DOM + screenshot). It returns pass/fail with a reason.
- `action:` — deterministic: `navigate`, `click: {selector|text}`, `fill`, `press`, `wait: {selector|text|ms}`, `screenshot`, `eval_js`, `assert_url`, `assert_text`.

### Assertion types (all targets)
| Type | Example |
|---|---|
| `judge` | `judge: "..."` or `judge: { rubric: "...", model: ..., scale: binary\|1-5, min: 4 }` |
| `contains` / `not_contains` | on the full agent transcript |
| `regex` | `regex: "order #\\d{6}"` |
| `json_path` | REST only: `{ path: $.status, equals: ok }` |
| `status` | REST only: HTTP status of every turn |
| `tool_called` / `tool_not_called` | when the target exposes tool calls (REST `response.tool_calls`, LiveKit agent attributes/data) |
| `dtmf_received` | sip/livekit audio: digits the agent sent, `{ equals: "1234#" }` |
| `call_ended_by` | sip: `agent`, `caller`, `timeout`, `error`, `no_answer`, `busy` |
| `transferred_to` | sip: target of a SIP REFER (`{ matches: regex }`) |
| `evaluator` | any registered Genkit evaluator by name, e.g. `{ evaluator: genkitEval/faithfulness, min: 0.8 }` |
| `metric` | `{ name, lt\|lte\|gt\|gte\|eq }` against built-in metrics (section 8) |
| `turns` | `{ lte: 6 }` |
| `custom` | Go-registered assertion by name |

Assertions can be attached to a whole test (`assert:`), or to a single turn with `on_turn: N` / `on_turn: last`.

---

## 6. Go API

```go
package support_test

import (
    "testing"

    "github.com/xavidop/tamesu"
    "github.com/xavidop/tamesu/assert"
    "github.com/xavidop/tamesu/llm"
    "github.com/xavidop/tamesu/targets/livekit"
)

func TestReschedule(t *testing.T) {
    voice := livekit.New(livekit.Config{
        URL:  tamesu.Env("LIVEKIT_URL"),
        Room: "support-" + tamesu.UUID(),
        Mode: livekit.Audio,
    })

    tamesu.Run(t, tamesu.Test{
        Target:  voice,
        Persona: tamesu.Persona{Name: "Alice", Style: "impatient", Voice: "en-GB-female"},
        Scenario: tamesu.Scenario{Goal: "Move Friday booking to Saturday", MaxTurns: 12},
        Repeat:  5, PassThreshold: 0.8,
        Assert: []assert.Assertion{
            llm.Judge("Agent confirmed new date and did not invent availability"),
            assert.ToolCalled("reschedule_booking"),
            assert.Metric("time_to_first_audio_ms").Lt(1500),
        },
    })
}
```

- `tamesu.Run` integrates with `testing.T`: subtests per repeat, `t.Log` for transcripts, artifacts written to `t.TempDir()` or `TAMESU_OUT`.
- `tamesu.LoadSuite("suite.yaml")` lets Go code run YAML suites, so `go test` alone can drive everything.
- Extension points are exported interfaces: `Target`, `Assertion`, `Reporter`, `MetricCollector`.
- `llm.WithGenkit(g *genkit.Genkit)` lets a test reuse the caller's own Genkit instance (its plugins, models, tracing) instead of tamesu creating one. This is how a Genkit app tests its own agent in-process.

---

## 7. Architecture

The repo is a **multi-module monorepo**, the same way tobira is: a light core library module at the root, and each heavy dependency set (Genkit, LiveKit/pion, chromedp, CGO audio) in its own `go.mod`. Users who embed the core never pull the heavy SDKs, and every module is built and tested on its own with `GOWORK=off`. A `go.work` file ties them together for local development.

```
doc.go                 package docs for pkg.go.dev (root module: github.com/xavidop/tamesu)
tamesu.go              public API: Run, Test, Persona, Scenario, LoadSuite
target/                public Target / Session / BrowserSession interfaces (root module)
assert/                built-in deterministic assertions (root module)
metrics/               latency, audio and conversation metrics (root module)
report/                console, junit, json, html (embedded static assets via go:embed) (root module)
internal/config/       tamesu.yaml + suite loading, interpolation, JSON Schema generation
internal/runner/       scheduler, parallelism, repeats, retries, budgets, cancellation
internal/persona/      simulated-user loop (text): next utterance, stop detection
internal/secrets/      mamori integration: collect refs, resolve once per run, hold secret.String values
internal/redact/       secret redaction for transcripts, HARs and reports (fed by internal/secrets)
schema/                generated JSON Schema (tamesu.schema.json)

cmd/tamesu/            CLI (cobra)                                  — own go.mod, the only module that pulls everything
llm/                   the only place AI happens: Genkit init, plugins, flows, dotprompt files, judge,
                       evaluators, cost accounting, speech (STT/TTS through Genkit models) — own go.mod
llm/prompts/           *.prompt dotprompt files (simulator, judge, browser_agent, expect, stop_when, scenario_gen)
llm/plugins/deepgram/  Genkit model plugin: deepgram/* STT models (audio in → text) — own go.mod
llm/plugins/elevenlabs/ Genkit model plugin: elevenlabs/* TTS models (text → audio media part) — own go.mod
targets/rest/          HTTP turns, SSE, WebSocket, JSONPath extraction — own go.mod
targets/livekit/       room join, text mode, audio mode, agent detection — own go.mod
targets/sip/           phone calls via LiveKit SIP: dial out, answer in, DTMF, transfers, call events — own go.mod
targets/browser/       chromedp driver, agent loop (Genkit tools), action cache, deterministic actions — own go.mod
audio/                 VAD (silero via onnxruntime-go), resampling, G.711 line simulation, WAV writer,
                       noise mixer (CGO) — own go.mod
targettest/            conformance suite every target must pass (like tobira's drivertest)

examples/              runnable suites + a tiny Genkit REST agent + a LiveKit agent + an embedding example
skills/tamesu/         SKILL.md agent skill (installable with `npx skills add xavidop/tamesu`)
site/                  Astro docs site, deployed to GitHub Pages (section 12)
assets/                tamesu-logo.svg, tamesu-wordmark.svg
scripts/               completions.sh, demo-seed.sh
```

### Core interfaces

```go
type Target interface {
    Name() string
    Start(ctx context.Context, run *RunContext) (Session, error)
}

// Conversational targets (rest, livekit, sip)
type Session interface {
    Send(ctx context.Context, turn UserTurn) error        // text or audio
    Receive(ctx context.Context) (AgentTurn, error)       // blocks until the agent turn is complete
    Close(ctx context.Context) (Artifacts, error)
}

// Browser target exposes a Page instead of Send/Receive
type BrowserSession interface {
    Page() Page
    Close(ctx context.Context) (Artifacts, error)
}

type Assertion interface {
    Name() string
    Evaluate(ctx context.Context, r *RunResult) AssertionResult
}
```

### 7.1 AI layer: Genkit everywhere

Every model call in tamesu is a Genkit call made from the `llm` module. No other module imports a model provider SDK, and `check_root_deps.sh` (extended as `check_ai_deps.sh`) fails CI if one does.

- **One Genkit instance per run**, created with `genkit.Init(ctx, genkit.WithPlugins(...), genkit.WithPromptDir("llm/prompts"))` (prompts are embedded with `go:embed` and loaded from an `fs.FS`). Plugins are initialised only if listed in `genkit.plugins` and their credentials resolved through mamori.
- **Models are Genkit references** everywhere in config (`googleai/…`, `anthropic/…`, `openai/…`, `ollama/…`, `deepgram/…`, `elevenlabs/…`), so switching provider is a one-line change.
- **Flows** (`genkit.DefineFlow`), each visible as a span in traces:
  - `simulateUserTurn` — persona + scenario + history → next utterance, or `stop`. Structured output: `{ say, dtmf?, hang_up?, stop, reason }`.
  - `judgeRun` — rubric + transcript + tool calls + metrics → `{ pass, score, reason, evidence[] }` via `ai.WithOutputType`.
  - `browserStep` — observation → actions. The browser actions (`click`, `type`, `select`, `scroll`, `navigate`, `wait`, `done`) are Genkit **tools** (`genkit.DefineTool`), and the agent loop is Genkit's tool-calling loop with `ai.WithMaxTurns(15)`. Screenshots go in as media parts.
  - `checkExpect` — page state → `{ pass, reason }` for `expect:` steps.
  - `generateScenarios` — `tamesu generate --from prd.md --target api` turns a PRD, system prompt or knowledge base into draft YAML tests (personas, goals, judges) for human review.
  - `transcribe` / `synthesize` — STT and TTS (below).
- **Prompts are dotprompt files** in `llm/prompts/` with input/output schemas and a version in their front matter. The prompt version is recorded in `result.json` so judge drift is traceable. Users can override any prompt by placing a file with the same name in `.tamesu/prompts/`.
- **Speech through Genkit models**: TTS is `genkit.Generate` with a TTS model returning an audio media part (Gemini TTS or OpenAI TTS natively; ElevenLabs via the bundled `elevenlabs` plugin). STT is `genkit.Generate` with an audio media part in and text out (Gemini audio input natively; Deepgram via the bundled `deepgram` plugin, with a streaming variant for low latency). Both bundled plugins are ordinary Genkit model plugins, so they also work in anyone's Genkit app.
- **Evaluators**: judges are also registered as Genkit evaluators (`genkit.DefineEvaluator`), and any Genkit evaluator (e.g. the `evaluators` plugin's faithfulness, answer relevancy, maliciousness) can be used as an `evaluator:` assertion.
- **Observability**: with `genkit.dev_ui: true` (or running under `genkit start -- tamesu run …`) every run's traces show up in the Genkit Developer UI. Trace IDs are always written to `result.json` and linked from the HTML report. OpenTelemetry export works through Genkit's standard telemetry plugins.
- **Cost accounting** reads token usage from every `ai.ModelResponse` (`Usage`) through a Genkit middleware, prices it from a table in `llm/pricing.yaml`, and enforces budgets.
- **Fake provider for CI**: a Genkit plugin `llmfake` (build tag `llmfake`) registers deterministic models (`fake/simulator`, `fake/judge`, `fake/browser`, `fake/tts`, `fake/stt`) that replay fixtures, so the whole pipeline is tested without network or keys.
- The example agents in `examples/` are themselves built with Genkit (`rest-agent`, and a LiveKit agent whose LLM step is a Genkit flow), so tamesu dogfoods the stack it recommends.

### Flow of a conversational test
1. The runner resolves the target and creates a `Session`.
2. The persona loop runs: the simulator LLM produces the next user message from persona + scenario + history → `Send` → `Receive` → append to the transcript → check `stop_when` / `max_turns` / agent `done`.
3. Metrics are collected all through the loop. Timestamps come from the transport layer, never from the LLM.
4. The session closes and artifacts are saved.
5. Assertions are evaluated. Judges get the transcript, tool calls and metrics as structured input.
6. The result is aggregated across repeats and checked against `pass_threshold`.

### LiveKit target details
- Use `github.com/livekit/server-sdk-go/v2` (room client + token generation) and `github.com/livekit/protocol`.
- Create a token for the identity `tamesu-sim-<uuid>`, join the room, and optionally request agent dispatch.
- **Text mode**: send user turns as LiveKit text streams / data messages on `data_topic`, and read agent replies the same way plus the transcription streams. Detect the end of an agent turn with the agent's `lk.agent.state` attribute (`speaking` → `listening`), with an idle timeout as fallback.
- **Audio mode**:
  - The `synthesize` Genkit flow (TTS model) produces the user turn → PCM 48 kHz mono → publish as a local audio track (Opus via pion).
  - Subscribe to the agent's audio track → decode → VAD for agent speech start/end → the `transcribe` Genkit flow (STT model) for the transcript (or use the agent's own transcription stream when available, and record which source was used).
  - Interruptions: when the persona decides to interrupt, start publishing the next user audio while the agent is still speaking. Measure how long the agent takes to stop.
  - Background noise: mix noise presets into outgoing PCM at a configurable SNR.
  - Record a stereo WAV (L = user, R = agent) for every run.
- Wait for the agent participant (by `agent_identity_prefix` or `kind=agent`), with a configurable `join_timeout`.

### SIP target details (v1)

SIP reuses the LiveKit audio pipeline: the call is bridged into a LiveKit room by the LiveKit SIP service, and tamesu talks to it from inside that room exactly like in audio mode. The SIP-specific work is call control, line simulation and call-level metrics. This lets tamesu test any phone agent (Voiceflow, Vapi, Retell, Twilio-based IVRs, Agentforce, a human-staffed queue…) without the agent knowing it's being tested.

- **Requirements**: a LiveKit server with the SIP service (`livekit/sip`, self-hosted or LiveKit Cloud) and a SIP trunk. `tamesu sip setup` creates or reuses the outbound trunk and the inbound trunk + dispatch rule through the LiveKit SIP API (`github.com/livekit/server-sdk-go/v2` `SIPClient`), with credentials from mamori, and prints the IDs to put in `tamesu.yaml`. It is idempotent.
- **Outbound (tamesu calls the agent)**: create a room `tamesu-sip-<uuid>`, join as the simulator, then `CreateSIPParticipant` with the trunk, `to`, `from`, `wait_until_answered` and `ringing_timeout`. Call setup and ring time come from the SIP participant's state attributes (`sip.callStatus`: dialing → ringing → active → hangup).
- **Inbound (the agent calls tamesu)**, for outbound campaigns and callbacks: tamesu creates a room matching the dispatch rule, fires the `trigger` (an HTTP request, or a shell command) that makes the agent dial `sip.inbound_number`, waits for the SIP participant to join, then runs the conversation. The test fails with `call_ended_by: no_answer` if nothing arrives within `ring_timeout`.
- **Conversation**: identical to LiveKit audio mode — `synthesize` → publish; subscribe → VAD → `transcribe`. Interruptions, silence and background noise all work the same.
- **DTMF**: the persona's structured output can include `dtmf`, and scripts can use `dtmf:` steps. tamesu sends them with LiveKit's SIP DTMF (RFC 2833/4733 via the SIP participant) and records digits the agent sends for `dtmf_received`.
- **`wait_for` / `say` scripts**: deterministic steps for IVRs (`wait_for` fuzzy-matches the streaming transcript with a timeout), freely mixed with persona-driven turns.
- **Line simulation** (`line_quality`): `pstn` resamples to 8 kHz and applies G.711 μ-law/A-law, plus optional jitter/packet-loss profiles (`mobile_poor`, `voip_good`), so the agent hears what a real caller would. Applied on the outgoing audio before publishing.
- **Transfers and hangups**: record SIP REFER targets (`transferred_to`), who hung up and why (`call_ended_by`), and the SIP status code on failures (486 busy, 480 unavailable, 408 timeout…).
- **Concurrency and cost safety**: calls are capped by `sip.max_concurrent_calls` (default 2), every call has `max_call_duration`, and `tamesu run` refuses to dial numbers outside `sip.allowed_numbers` (E.164 patterns; required when the target number came from a variable) so a typo can't call a stranger.
- **Recordings**: stereo WAV as in audio mode, plus a call timeline (dial, ring, answer, DTMF, transfer, hangup) in the HTML report.
- **CI without a carrier**: the integration suite runs `livekit-server`, `livekit/sip` and an Asterisk container acting as the "carrier" and the agent's PBX, with the example LiveKit agent answering calls, so SIP is tested end to end with no phone number or trunk account.

### Browser target details
- Driver: `github.com/chromedp/chromedp` (pure Go, CDP). Chromium is auto-downloaded on first run if it's missing (cache in `~/.cache/tamesu`).
- Agent loop per `goal`:
  1. Observe: accessibility tree (pruned, with stable element refs) + optional screenshot + URL + recent console errors.
  2. Plan: the `browserStep` Genkit flow calls the model with the observation; the browser actions (`click(ref)`, `type(ref, text)`, `select`, `scroll`, `navigate`, `wait`, `done(success, reason)`) are Genkit tools.
  3. Act: each tool call executes through CDP and returns the new observation as the tool result. Max tool turns per goal default to 15.
- **Action cache** (key = hash of goal text + URL path + simplified DOM fingerprint): store the resolved deterministic actions. In `--replay` mode the cache is executed without the LLM, and only cache misses or failures fall back to the agent (or fail in strict replay). This is what keeps CI fast and cheap.
- Artifacts: a screenshot per step, a final HAR, console logs, and a step timeline.

### REST target details
- Turn templating from `request.body`, response extraction via JSONPath (`github.com/ohler55/ojg`).
- SSE: concatenate deltas until stream end; time to first token = first delta.
- WebSocket: configurable message templates and an end-of-turn detector (`done` JSONPath or idle timeout).

---

## 8. Metrics

| Metric | Targets | Definition |
|---|---|---|
| `turn_latency_ms` (p50/p95/max) | rest, livekit, sip | user turn sent → agent turn complete |
| `time_to_first_token_ms` | rest (sse/ws) | user turn sent → first delta |
| `time_to_first_audio_ms` | livekit audio, sip | user speech end (VAD) → agent speech start (VAD) |
| `interruption_stop_ms` | livekit audio, sip | user starts talking over the agent → agent audio stops |
| `talk_over_ratio` | livekit audio, sip | overlapping speech time / total agent speech time |
| `silence_gap_max_ms` | livekit audio, sip | longest dead air while a reply was expected |
| `call_setup_ms` | sip | dial (or trigger, inbound) → call active |
| `ring_ms` | sip | ringing → answered |
| `greeting_ms` | sip | call active → agent's first speech |
| `call_duration_ms` | sip | active → hangup |
| `stt_word_error_rate` | livekit audio, sip | only when the agent's own transcript is available: its transcript of the caller vs. the text tamesu synthesized |
| `turns` | conversational | number of agent turns |
| `goal_steps` | browser | agent steps used per goal |
| `llm_cost_usd`, `llm_tokens` | all | spend of tamesu's own LLM calls (simulator/judge/browser agent) |

Metric names follow the pattern `name` or `name_p95`.

---

## 9. Reports and artifacts

Run directory layout:
```
.tamesu/runs/2026-10-31T14-20-00/
  summary.json
  junit.xml
  report.html
  tests/<test-slug>/run-<n>/
    transcript.json
    audio.wav            (voice audio mode)
    screenshots/*.png    (browser)
    trace.har            (browser)
    result.json          (assertions, metrics, judge reasoning, genkit trace ids)
```
- **console**: live progress, a per-test pass/fail line, and on failure the judge's reason and the last 3 turns.
- **html**: a single self-contained file (go:embed) with transcripts, an audio player with turn markers, a screenshot timeline and metrics tables.
- **junit**: one testcase per test, with repeats aggregated. Failure messages include the judge reasons.

---

## 10. Determinism, cost and safety

- `seed` per test, passed to the simulator prompts and the persona behavior RNG.
- Judge prompts are versioned and return structured output only (`pass`, `score`, `reason`, `evidence[]`). Use temperature 0 when the provider supports it.
- Every LLM call goes through the `llm` module, which counts tokens and estimated cost and enforces `--max-cost-usd`.
- Secrets are never written to artifacts. Every value resolved through mamori (section 4.1) is registered with `internal/redact`, which replaces it (and its base64 and URL-encoded forms) with `‹secret:NAME›` in transcripts, HARs, screenshots' DOM snapshots, logs, Genkit traces and reports. As a second line of defence, `${VAR}` values whose name matches `*_KEY`, `*_SECRET`, `*_TOKEN` or `*PASSWORD*` are redacted too. Secrets are never sent to the simulator or judge models: the browser agent sees `{{secrets.NAME}}` placeholders and tamesu substitutes the real value only inside the CDP `type` action.
- The browser agent refuses navigation outside `allowed_domains` (default: the target URL's domain).

---

## 11. Dependencies (suggested)

- CLI: `github.com/spf13/cobra`
- YAML: `gopkg.in/yaml.v3` + `github.com/invopop/jsonschema` for schema generation
- Secrets: `github.com/xavidop/mamori` (core, in the root module) + `github.com/xavidop/mamori/providers/{aws,vault,gcp,azure,k8s,onepassword,doppler,infisical,sops}` (only in `cmd/tamesu`)
- AI: `github.com/firebase/genkit/go` only — `genkit`, `ai`, `plugins/googlegenai`, `plugins/anthropic`, `plugins/compat_oai/openai`, `plugins/ollama`, `plugins/evaluators`, the dotprompt support built into Genkit, plus tamesu's own `llm/plugins/deepgram` and `llm/plugins/elevenlabs` Genkit plugins. No direct provider SDKs anywhere else.
- LiveKit + SIP: `github.com/livekit/server-sdk-go/v2` (rooms, tokens, `SIPClient`), `github.com/livekit/protocol`, `github.com/pion/webrtc/v4`; `livekit/sip` and Asterisk images for integration tests
- Audio: G.711 μ-law/A-law codec (pure Go) for line simulation
- Audio: `github.com/yalue/onnxruntime_go` (Silero VAD), an Opus codec binding, resampling
- Browser: `github.com/chromedp/chromedp`, `github.com/chromedp/cdproto`
- JSONPath: `github.com/ohler55/ojg`
- Testing the framework itself: `github.com/stretchr/testify`, `httptest`, plus LiveKit server, LiveKit SIP and Asterisk in Docker for integration tests

Keep CGO optional. Audio and SIP may require CGO (onnxruntime, opus). Release two builds: `tamesu` (full) and `tamesu-lite` (no audio mode and no SIP, pure Go, fully static).

---

## 12. Repository, automation and docs site

The repository follows the same shape, tooling and conventions as [xavidop/tobira](https://github.com/xavidop/tobira). When in doubt, copy what tobira does.

### 12.1 Root files

```
.github/                 (12.2)
.gitignore
.golangci.yml            golangci-lint config, shared by every module
.goreleaser.yaml         (12.4)
.releaserc.json          semantic-release config (12.4)
CHANGELOG.md             generated by semantic-release, newest first
CODE_OF_CONDUCT.md
CONTRIBUTING.md          repo layout, build/test, commit conventions, how to add a target
LICENSE                  MIT © Xavier Portilla Edo
Makefile                 (12.3)
README.md                (12.6)
SECURITY.md              how to report vulnerabilities, what the redaction and allowed_domains guards do and do not cover
demo.tape                VHS script that records site/public/demo.gif
doc.go
go.mod / go.sum
go.work / go.work.sum
```

### 12.2 `.github/`

```
.github/
  CODEOWNERS                       * @xavidop
  FUNDING.yml
  PULL_REQUEST_TEMPLATE.md         summary, type (feat/fix/docs…), checklist: make all, docs updated, conformance suite
  dependabot.yml                   one gomod entry per module (root, cmd/tamesu, llm, llm/plugins/*, targets/*, audio),
                                   plus npm for /site and github-actions; weekly; labelled per module
  ISSUE_TEMPLATE/
    config.yml                     blank issues off, link to docs + discussions
    bug_report.yml                 version (`tamesu version`), OS, target type, suite snippet, run output
    feature_request.yml
    target_request.yml             a new target/transport (e.g. Pipecat, OpenAI Realtime, WhatsApp)
  scripts/
    check_dependabot_coverage.py   fails if any go.mod (or site/package.json) has no dependabot entry
    check_root_deps.sh             fails if the root module imports Genkit, LiveKit, pion, chromedp or CGO deps
    check_ai_deps.sh               fails if any module other than llm/ imports a model provider SDK
                                   (genai, openai, anthropic, deepgram, elevenlabs clients) instead of going through Genkit
    check_readme_claims.sh         fails if README lists a command/flag/target the CLI does not have
    check_docs_claims.sh           same check against site/src/pages/docs
  workflows/
    ci.yml
    pages.yml
    release.yml
```

#### `ci.yml` (push to main, pull_request, workflow_dispatch; `permissions: contents: read`; `GOWORK: off`)
- **discover**: find every `go.mod` (excluding `site/` and `testdata/`), output a JSON matrix.
- **test** (matrix over modules): `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`, `go test -race -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...`, coverage line in the step summary.
- **lint** (matrix over modules): `golangci-lint run` and `golangci-lint run --build-tags integration`.
- **fmt**: `gofmt -l .` must be empty.
- **checks**: run every `.github/scripts/check_*` script.
- **schema**: regenerate `schema/tamesu.schema.json` and fail on a diff.
- **integration**: start `livekit-server --dev`, `livekit/sip`, Asterisk (as carrier/PBX), Redis for SIP, headless Chromium and the example agents (docker compose in `examples/compose.yaml`); run `tamesu run examples/**/*.yaml --reporter junit` with a fake model provider (`-tags llmfake`, see 12.3) so CI needs no API keys; publish the JUnit report.
- **smoke**: build `cmd/tamesu` and run `tamesu version`, `tamesu init` and `tamesu validate` in a temp dir.

#### `pages.yml` (push to main on `site/**` or the workflow itself, workflow_dispatch)
- `permissions: contents: read, pages: write, id-token: write`; `concurrency: { group: pages, cancel-in-progress: false }`.
- build: setup-node 22 with npm cache on `site/package-lock.json` → `actions/configure-pages` → `npm ci` → `npm run build` with `BASE_PATH` from configure-pages → `npm run linkcheck` with the same `BASE_PATH` → `upload-pages-artifact` from `site/dist`.
- deploy: `actions/deploy-pages` to the `github-pages` environment.

#### `release.yml` (push to main, plus workflow_dispatch with an optional existing `tag` input)
1. Build a snapshot of `cmd/tamesu` and smoke-test `tamesu version` before tagging anything.
2. semantic-release analyses Conventional Commits, picks the next version, updates `CHANGELOG.md`, and creates and pushes `vX.Y.Z`.
3. GoReleaser builds and publishes the release (12.4).
4. SLSA provenance for the released artifacts.
- `permissions: contents: write, issues: write, pull-requests: write, id-token: write`.
- Running it by hand with `tag` skips semantic-release and only re-publishes that existing tag.
- Audio mode needs CGO, so the full `tamesu` build runs on native runners per OS/arch (ubuntu-24.04, ubuntu-24.04-arm, macos-15-intel, macos-latest, windows-latest) with `goreleaser build --single-target`, and a publish job uploads them. `tamesu-lite` is cross-compiled with `CGO_ENABLED=0` in one job.

### 12.3 `Makefile`

Multi-module, like tobira's: discover modules with `find . -name go.mod` (excluding `site/`, `node_modules/`, `testdata/`), export `GOWORK=off`, loop over them.

```
make help              list targets
make all               fmt vet check-deps check-claims schema-check test lint lint-integration test-llmfake
make build | test | race | vet | lint | lint-integration | tidy
make fmt               fail and list files if not gofmt-clean (never rewrites)
make work-sync         regenerate go.work, then tidy every module
make check-deps        .github/scripts/check_root_deps.sh and check_ai_deps.sh
make check-claims      README and docs claim checks
make schema | schema-check
make test-llmfake      end-to-end tests with the fake model provider (build tag llmfake: deterministic
                       simulator and judge responses, no network). A default build never contains it.
make integration       docker compose up (livekit server, livekit/sip, Asterisk, example agents) and run the integration suites
make genkit-ui         run an example suite under `genkit start` to inspect traces in the Genkit Developer UI
make site-dev | site-build | site-linkcheck
make demo              run `vhs demo.tape` to refresh site/public/demo.gif
make clean
```

### 12.4 Releases

- **`.releaserc.json`**: branches `["main"]`, `tagFormat: "v${version}"`, plugins commit-analyzer, release-notes-generator, changelog (`CHANGELOG.md` with a short title paragraph), and git (commits `CHANGELOG.md` with `chore(release): ${nextRelease.version} [skip ci]`).
- **Conventional Commits** are required (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, `ci:`, with scopes like `feat(livekit):`). CONTRIBUTING.md documents them.
- **`.goreleaser.yaml`** (version 2, project `tamesu`):
  - `before.hooks`: `./scripts/completions.sh` (bash, zsh, fish, powershell).
  - builds: `tamesu` (CGO, native runners) and `tamesu-lite` (`CGO_ENABLED=0`, `-tags lite`); goos linux/darwin/windows, goarch amd64/arm64; ldflags `-s -w -X main.version -X main.commit -X main.date`; `GOWORK=off`.
  - archives `tamesu_{{ .Version }}_{{ .Os }}_{{ .Arch }}` (zip on Windows) with completions, README and LICENSE.
  - checksums, SBOMs, changelog from the release notes.
  - Homebrew cask in `xavidop/homebrew-tap` (`brew install xavidop/tap/tamesu`), Scoop manifest in `xavidop/scoop-bucket`, nfpms for deb/rpm/apk with completions.
  - A Docker image `ghcr.io/xavidop/tamesu` with Chromium preinstalled, for CI users.

### 12.5 Docs site (`site/`)

Plain Astro (not Starlight), structured like tobira's site:

```
site/
  astro.config.mjs      site + base from BASE_PATH (always normalised to a trailing slash);
                        @astrojs/sitemap; remark plugins remarkBaseLinks (prefix root-relative Markdown
                        links with the base) and remarkTableCode (keep short inline code in tables on one line)
  package.json          astro, @astrojs/sitemap, @astrojs/markdown-remark, unist-util-visit,
                        @fontsource-variable/inter, @fontsource-variable/plus-jakarta-sans, @fontsource/ibm-plex-mono;
                        scripts dev, build, preview, linkcheck
  package-lock.json
  tsconfig.json
  scripts/check-links.mjs   crawl dist/, strip BASE_PATH, fail on any broken internal link or anchor
  public/
    favicon.svg         the tamesu mark (seal only)
    demo.gif            recorded with demo.tape
    brand/              logo, wordmark, social card
  src/
    components/Search.astro
    layouts/Layout.astro, DocsLayout.astro
    lib/docs-content.ts
    styles/global.css   indigo #1E2A5A / vermilion #E5482F / off-white #F7F3EA brand tokens, light and dark
    pages/
      index.astro       landing page: tagline, demo, four cards (browser, REST, voice, phone), install, quick start
      llms.txt.ts       and llms-full.txt.ts for AI agents
      robots.txt.ts
      docs/             one .md page per topic, plus a folder per topic for sub-pages
```

Docs pages (`site/src/pages/docs/`):
`index`, `quickstart`, `install`, `concepts`, `configuration`, `suites` (YAML reference, generated from the JSON Schema), `personas-and-scenarios`, `generate` (scenarios from a PRD), `assertions`, `judges`, `metrics`, `targets/` (`rest`, `livekit`, `sip`, `browser`), `sip-setup` (LiveKit SIP service, trunks with Twilio / Telnyx, inbound vs outbound, allowed numbers), `ai` (Genkit: models, plugins, prompts and overriding them, evaluators, Developer UI, cost), `go-api`, `reports`, `ci` (GitHub Actions, GitLab, Docker image), `cost-and-determinism`, `security`, `write-a-target`, `cli` (every command, flag and exit code), `mcp` (phase 7), `troubleshooting`, `roadmap`, `contributing`.

### 12.6 README

Same shape as tobira's README:
1. Centered logo (`site/public/favicon.svg`), `# 試す tamesu`, tagline ***Put your web apps and AI agents to the test.***, one-line description.
2. Badges: Go Reference, CI, License MIT.
3. One paragraph on the name (試す, Japanese for *to try / to put to the test*), then `demo.gif`.
4. **Why**, **Install** (Homebrew, Scoop, Linux packages, release binaries, Docker, `go install`), **Quick start**, **Features**, **Targets** table (target, transport, modes, metrics, tested against), **CI**, **Go API**, **Agent skill**, **Documentation** links into the site, **Contributing** (`make all`), **License**.

### 12.7 Agent skill

`skills/tamesu/SKILL.md` teaches coding agents the whole CLI: writing suites, every assertion, targets and their config, running in CI, reading reports and debugging failures. Its frontmatter `description` lists every command and concept so it triggers reliably. The README and docs show `npx skills add xavidop/tamesu`.

---

## 13. Phased plan

### Phase 0 — Skeleton
- Go modules `github.com/xavidop/tamesu` (root) and `cmd/tamesu`, `go.work`, cobra CLI, `init` / `validate` / `list` / `version`.
- Config + suite structs, interpolation, generated JSON Schema, `internal/secrets` + `internal/redact` on mamori, `tamesu doctor`.
- The whole repository scaffolding from section 12: root files, `.github/` (CI, Pages, release, dependabot, templates, check scripts), Makefile, `.golangci.yml`, `.goreleaser.yaml`, `.releaserc.json`, the Astro `site/` with landing page, quickstart, install and CLI pages, `assets/` and `site/public/` with the logo, README, and a first `skills/tamesu/SKILL.md`.
- **Accept**: `tamesu init && tamesu validate` works; invalid suites produce errors with line numbers; exit code 2 on schema errors; `make all` passes; `make site-build site-linkcheck` passes with and without `BASE_PATH=/tamesu`; a GoReleaser snapshot (`goreleaser release --snapshot --clean`) produces archives and packages.

### Phase 1 — Core runner, Genkit layer + REST target
- Runner (parallel, repeat, retries, timeouts, budgets), persona loop.
- The `llm` module from section 7.1: Genkit init from config and mamori secrets, dotprompt files, the `simulateUserTurn` and `judgeRun` flows, Genkit evaluators, cost middleware, the `llmfake` plugin, trace IDs in results, `genkit.dev_ui`.
- REST target (plain JSON, SSE). Assertions: judge, evaluator, contains, regex, json_path, status, turns, tool_called, metric.
- Console + JSON + JUnit reporters.
- `examples/rest-agent/`: a tiny Go HTTP agent built with Genkit, plus a suite that tests it.
- **Accept**: the example suite passes against the example agent; a deliberately broken agent fails with a readable judge reason; JUnit output loads in GitHub Actions test reporting; the same suite passes under `-tags llmfake` with no network; traces appear in the Genkit Developer UI with `make genkit-ui`; `check_ai_deps.sh` passes.

### Phase 2 — LiveKit text mode
- Room join, token generation, agent detection, data/text-stream turns, end-of-turn via agent state.
- `examples/livekit-agent/` plus a docker-compose with `livekit-server --dev`.
- **Accept**: a text-mode voice test runs end-to-end locally and in CI using the dev server.

### Phase 3 — LiveKit audio mode
- `synthesize` / `transcribe` Genkit flows (Gemini TTS + audio-input STT natively), the `deepgram` and `elevenlabs` Genkit plugins, publish track; subscribe → VAD → STT; stereo WAV recording; interruptions; noise mixing.
- Audio metrics from section 8.
- **Accept**: `time_to_first_audio_ms` and `interruption_stop_ms` are reported, within ±50 ms of reference timings measured on a known recording; the WAV plays back correctly in the HTML report; switching TTS/STT provider is only a config change.

### Phase 4 — SIP target
- `targets/sip` on top of the audio pipeline: `tamesu sip setup`, outbound dialing, inbound answering with triggers, DTMF send/receive, `wait_for`/`say` scripts, line simulation (G.711, jitter, loss), transfers, hangup reasons, call timeline, `allowed_numbers` and concurrency guards.
- SIP metrics and assertions (`call_setup_ms`, `ring_ms`, `greeting_ms`, `dtmf_received`, `call_ended_by`, `transferred_to`).
- `examples/compose.yaml` with livekit-server, livekit/sip, Redis and Asterisk; an example IVR suite.
- **Accept**: outbound and inbound example calls pass end to end in CI through Asterisk with no real carrier; a DTMF menu is navigated; a call to a number outside `allowed_numbers` is refused with exit 2; a manually triggered workflow run against a real Twilio trunk passes (documented, not run on every PR).

### Phase 5 — Browser target
- chromedp driver, observation (a11y tree + screenshot), the `browserStep` flow with browser actions as Genkit tools, deterministic actions, `expect` checks via `checkExpect`, artifacts.
- **Accept**: example suites pass against a bundled demo web app (served by the CLI's test helper); failure reports include the screenshot at the failing step.

### Phase 6 — Action cache, replay, recorder, generator, HTML report (v1.0)
- Action cache, `--replay` / `--update-cache`, `tamesu record browser`, `tamesu generate` (the `generateScenarios` flow), full HTML report.
- **Accept**: a second run of a browser suite with `--replay` makes zero LLM calls and runs at least 5× faster than the first; `tamesu generate --from examples/rest-agent/PRD.md` produces a suite that `tamesu validate` accepts. Tag v1.0.0.

### Phase 7 — Extensions (post-v1)
- WebSocket and OpenAI Realtime targets, MCP server mode (`tamesu mcp`, built with Genkit's MCP support) so coding agents can run and author tests, red-team packs (prompt injection, PII leakage) as reusable assertion sets, load mode for concurrent calls.

---

## 14. Coding conventions for Claude Code

- Go 1.24+, `golangci-lint` clean, `context.Context` first on every blocking call, no global state except the Genkit instance in the `llm` module.
- Every AI call goes through Genkit in the `llm` module: flows, dotprompt files, tools, evaluators, Genkit model plugins for anything not supported natively. Never import a model provider SDK elsewhere (`check_ai_deps.sh` enforces this).
- Never add a heavy dependency to the root module; put it in the module that needs it (`check_root_deps.sh` enforces this). After adding a module, add its dependabot entry.
- Every commit follows Conventional Commits, because releases are cut from them.
- Every target must pass the `targettest` conformance suite.
- Everything that talks to the network goes behind an interface with a fake implementation in tests.
- Table-driven tests. Integration tests are tagged `//go:build integration` and need Docker.
- Errors are wrapped with `%w`. User-facing errors include the suite file, test name and YAML line.
- Use structured logs (`log/slog`) for everything. `-v` switches on the debug level.
- Keep packages small. Public API surface is only `tamesu`, `target`, `targets/*`, `assert`, `metrics` and `llm`.
- Update `README.md`, `site/src/pages/docs/`, `skills/tamesu/SKILL.md` and `CHANGELOG`-worthy commit messages with every phase. The claim-check scripts fail CI if the README or docs mention a command, flag or target the CLI does not have.
- Put the logo from `tamesu-logo.svg` in `assets/tamesu-wordmark.svg`, and the seal alone (first `<g>` of the logo, viewBox `0 0 200 200`) in `assets/tamesu-logo.svg` and `site/public/favicon.svg`.