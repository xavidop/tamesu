---
name: tamesu
description: Use when working with tamesu (試す), the Go agentic testing framework and CLI for web apps and AI agents, when writing or fixing a tamesu suite in YAML (targets of type browser, rest, grpc, livekit, sip, realtime or pipecat; personas, scenarios, steps, assertions such as judge, contains, not_contains, regex, not_regex, json_path, status, tool_called, tool_not_called, tool_sequence, captured, dtmf_received, call_ended_by, transferred_to, evaluator, metric, turns, custom, on_turn, and the red-team packs pack: prompt_injection / pii_leakage / system_prompt_leakage or suite-defined packs), configuring tamesu.yaml (genkit plugins, models, speech, sip, defaults, budgets, reporters, secrets via mamori refs such as env: aws-sm:// vault:// op://), running `tamesu init`, `tamesu validate`, `tamesu list`, `tamesu run` (filter, tags, target, parallel, repeat, load and load-duration for load mode, mode, headed, replay, update-cache, out, reporter, max-cost-usd), `tamesu doctor`, `tamesu report` (rebuild the HTML report of a run directory), `tamesu record` / `tamesu record browser` (record a browser session as steps), `tamesu generate` (draft tests from a PRD or system prompt with --from, --target, --type, --count, --out), `tamesu sip` / `tamesu sip setup` (LiveKit SIP trunks and dispatch rule for the phone target), `tamesu mcp` (serve tamesu to coding agents over the Model Context Protocol, stdio or --http, with --cwd; run directories as tamesu://runs resources) or `tamesu version`, reading its exit codes (0 passed, 1 failed, 2 config error, 3 infrastructure error, 4 budget exceeded), running it in CI, reading its reports and run directories, debugging a failing validation or an unresolved secret, or answering what the tamesu CLI can do. Covers github.com/xavidop/tamesu.
---

# tamesu: put your web apps and AI agents to the test

tamesu (試す, Japanese for *to try / to put to the test*) is a Go CLI and framework that runs
agentic tests against web apps and AI agents. A **persona** with a **scenario** plays the user;
the **target** is the system under test: `browser` (Chromium, driven by the browser agent on
`models.browser_agent`), `rest` (HTTP, SSE, websocket), `livekit` (voice over WebRTC, text and
audio), `sip` (phone calls through a LiveKit SIP service), `realtime` (the OpenAI Realtime
protocol, text and audio), `grpc` (an agent behind tamesu's turn API, `proto/tamesu/turn/v1`) or
`pipecat` (a Pipecat bot's websocket transport, text and audio); **assertions** mix deterministic checks with LLM **judges**; every run
leaves a transcript, metrics and reports. Module: `github.com/xavidop/tamesu`. Docs:
https://xavidop.github.io/tamesu/docs/

## The model in one minute

- A **suite** is a YAML file: `suite:` (name), `tags:`, `vars:`, `secrets:`, `targets:` (by
  name) and `tests:`.
- A **target** has a `type`: `browser` (a URL, `browser: {engine, viewport, locale}`,
  `auth.storage_state`, `allowed_domains`; goals run through the browser agent on an
  accessibility tree with numbered refs, secrets are typed only inside the browser), `rest` (URL, method,
  headers, how a user turn is sent with `request.body` and how the agent turn is read back
  with JSONPaths in `response.text`, `response.tool_calls`, `response.done`), `livekit` (server
  URL, `api_key`/`api_secret` from secrets, `room`, `dispatch.agent_name`,
  `agent_identity_prefix`, `mode: text|audio`, `data_topic`, `join_timeout`; text mode sends
  user turns on `lk.chat` and reads replies on `lk.transcription`, ending a turn on the agent's
  `lk.agent.state` or after 1.5 s idle; audio mode speaks with the TTS model and listens with
  the VAD and STT, `transcript_source: auto|agent|stt`) or `sip` (`direction: outbound|inbound`,
  `to`, `from`, `trunk_id`, `ring_timeout`, `max_call_duration`, an inbound `trigger` with
  `http` or `command`; the project's `sip:` block has `livekit_url`, `outbound_trunk_id`,
  `inbound_number`, `allowed_numbers` (required when `to` is a template) and `trunk` for
  `tamesu sip setup`).
- A **test** names a target. Browser tests have `steps` (`goal:` agentic, `expect:` agentic
  assertion, `action:` one of `navigate click fill press wait screenshot eval_js assert_url
  assert_text`). Conversational tests have a `persona` and a `scenario` with a `goal`,
  `max_turns`, `stop_when` and, for voice, a `script` of `wait_for` / `dtmf` / `say` steps.
- **Assertions** go under `assert:`. Exactly one kind per entry: `judge` (a rubric string, or
  `{ rubric, model, scale: binary|1-5, min }`), `contains`, `not_contains`, `regex`, `not_regex`,
  `json_path` (`{ path, equals }`, rest only), `status` (rest only), `tool_called` /
  `tool_not_called` (a name, or `{ name, args }`), `tool_sequence` (`{ calls: [a, b], exact }`),
  `captured` (`{ name, equals | matches }`), `dtmf_received` (`{ equals }`, sip and
  livekit audio), `call_ended_by` (sip: `agent caller timeout error no_answer busy`),
  `transferred_to` (`{ matches }`, sip), `evaluator` (a Genkit evaluator name, or `{ name, min }`),
  `metric` (`{ name, lt|lte|gt|gte|eq }`), `turns` (`{ lte: 6 }`), `custom` (a Go-registered
  name). Add `on_turn: N` or `on_turn: last` to check one agent turn.
- **Metrics** are built in: `turn_latency_ms` (with `_p50`, `_p95`, `_max`),
  `time_to_first_token_ms`, `time_to_first_audio_ms`, `interruption_stop_ms`,
  `talk_over_ratio`, `silence_gap_max_ms`, `call_setup_ms`, `ring_ms`, `greeting_ms`,
  `call_duration_ms`, `stt_word_error_rate`, `turns`, `goal_steps`, `llm_cost_usd`,
  `llm_tokens`.
- **Templates**: `{{uuid}}`, `{{now}}`, `{{env.X}}`, `{{vars.x}}`, `{{secrets.X}}`,
  `{{faker.email}}`, and in a REST body `{{session}}` and `{{message}}`. `${VAR}` and
  `${VAR:-default}` expand from the environment for non-secret values only.
- **Secrets** are mamori refs declared under `secrets:` in `tamesu.yaml` or the suite:
  `env:NAME`, `dotenv://.env#NAME`, `aws-sm://path#key`, `aws-ps://`, `vault://secret/data/x#key`,
  `gcp-sm://`, `azure-kv://`, `k8s-secret://`, `op://vault/item/field`, `doppler://`,
  `infisical://`, `sops://`, or a chain `env:X,aws-sm://ci/x#key` tried in order. `${VAR}`
  inside a ref is a ref var fed by `--var KEY=VAL` and `vars:`, never the environment. Use
  `{{secrets.NAME}}` in the suite. Never put a value in a suite; never use `${VAR}` for
  `api_key`, `api_secret`, `headers.Authorization`, `password` or `token` (validate warns).

## Writing a suite

Start from `tamesu init`, which writes `tamesu.yaml` and `examples/support-agent.yaml`. A
minimal REST suite:

```yaml
suite: support-agent
secrets:
  API_TOKEN: env:SUPPORT_API_TOKEN
targets:
  api:
    type: rest
    url: https://api.example.com/v1/chat
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request: { body: { session_id: "{{session}}", message: "{{message}}" } }
    response: { text: $.reply, tool_calls: $.tool_calls[*].name, done: $.end_session }
tests:
  - name: refund policy question
    target: api
    persona: { name: Marta, style: polite, slightly confused, language: es }
    scenario: { goal: "Find out if you can return shoes bought 40 days ago", max_turns: 8 }
    assert:
      - judge: "The agent states the 30-day return policy and does not promise an exception"
      - not_contains: "I'm just an AI"
      - metric: { name: turn_latency_p95_ms, lt: 4000 }
```

Rules the validator enforces (each error is `file:line: path: message`):

- a test's `target` must be a key of `targets`; browser tests need `steps` and no
  `persona`/`scenario`; conversational tests need `scenario.goal` and no `steps`;
- a step is exactly one of `goal`, `expect`, `action`; an action is exactly one known action;
- an assertion is exactly one kind; `metric` and `turns` take exactly one comparator; a `pack`
  entry (`pack: prompt_injection`, `pii_leakage`, `system_prompt_leakage`, or a name under the
  suite's `packs:`) expands into that set when the suite loads and its results read
  `<pack>/<assertion>`; packs cannot nest;
  `json_path`/`status` are rest only; `call_ended_by`/`transferred_to` are sip only;
- `repeat` is 1 or more, `pass_threshold` is in (0, 1], `mode` is `text` or `audio` and only on
  livekit, sip or realtime tests; `vars` (per-test {{vars.x}}), `cases` / `cases_file`
  (parameterised: one test per case, named `<name> [<case>]`, tagged case:<name>); `hooks`
  (`before_all` / `after_all` / `before_each` / `after_each`, each `command` or `http`, on the
  suite or in tamesu.yaml); `capture` (`{ name, regex | json_path }`: values from the agent's turns
  as `{{captured.<name>}}` in later script say steps, tool_results, hooks and assertions);
- every `{{secrets.X}}` must be declared; a `${VAR}` in a secret field is a warning;
- an unset `${VAR}` is a warning for `validate` (so CI without the variables can check suites)
  and an error for `doctor` (and the runner).

## Configuration (tamesu.yaml)

```yaml
version: 1
suites: ["tests/**/*.yaml"]          # what run/validate/list use when no paths are given
genkit: { plugins: [googlegenai, anthropic, openai, ollama, deepgram, elevenlabs], dev_ui: false }
models:                               # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  browser_agent: googleai/gemini-2.5-pro
  simulator: openai/gpt-4.1-mini
  scenario_generator: googleai/gemini-2.5-pro
speech:
  stt: { model: deepgram/nova-3, language: en }
  tts: { model: googleai/gemini-2.5-flash-preview-tts }
  vad: { provider: silero }
sip: { livekit_url: ${LIVEKIT_URL}, outbound_trunk_id: ST_xxx, inbound_number: "+34910000000", max_concurrent_calls: 2, allowed_numbers: ["+1800555*"] }
defaults: { timeout: 3m, repeat: 1, retries: 0, pass_threshold: 1.0 }
budgets: { max_cost_usd: 5, max_tokens: 2_000_000 }
livekit: { max_concurrent_rooms: 4 }
reporters: [console, junit, html]
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ANTHROPIC_API_KEY: env:ANTHROPIC_API_KEY,aws-sm://ci/tamesu#anthropic
```

Suites can override `defaults`, `models`, `budgets` and `secrets`. Model credentials are
picked up by their conventional names (`GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`DEEPGRAM_API_KEY`, `ELEVENLABS_API_KEY`) or from `models.credentials`.

## Commands

```text
tamesu init [dir] [--force]                 scaffold tamesu.yaml and examples/
tamesu validate [paths...] [--strict]       check tamesu.yaml and the suites; exit 2 on errors
tamesu list [paths...] [--json] [--filter re] [--tags a,b] [--target name]
tamesu run [paths...] [--filter re] [--tags a,b] [--target name] [--parallel N] [--repeat N | --load N [--load-duration D]]
           [--mode text|audio] [--headed] [--replay | --update-cache] [--out dir] [--reporter console,json,junit,html] [--max-cost-usd X]
tamesu doctor [paths...]                    config, suites, every secret (which chain link answered), models, speech, Chromium, LiveKit, SIP
tamesu report <run-dir>                     rebuild report.html (self-contained: transcripts, assertions, metrics, screenshots, recording, audio) from a run
tamesu record browser <url> [--out f]       open a window, record clicks/typing/navigation as action steps (passwords become {{secrets.PASSWORD}})
tamesu generate --from f --target t [--type rest|livekit|sip|browser] [--count N] [--out f]   draft tests from a PRD/system prompt; validates the draft
tamesu sip setup [--outbound] [--inbound]   create or reuse the LiveKit SIP trunks and the inbound dispatch rule from sip.trunk; prints the ids
tamesu mcp [--cwd dir] [--http addr]        serve tamesu over MCP (stdio, or HTTP with --http): tamesu_schema, tamesu_validate, tamesu_list, tamesu_run, tamesu_generate, tamesu_doctor, tamesu_init
tamesu version
```

`tamesu run` resolves only the secrets the selected tests use, runs repeats in parallel, retries
infrastructure errors (`retries`), stops at `budgets.max_cost_usd` (exit 4) and writes
`.tamesu/runs/<timestamp>/` with `summary.json`, `junit.xml` and per run
`tests/<test>/run-<n>/{transcript.json,result.json}`. `result.json` has every assertion's reason,
the metrics, the judge trace ids and prompt version. With `-v` each run prints a line.

The AI layer needs a model: `genkit.plugins` and `models.*` in `tamesu.yaml`, with the
credential declared under `secrets:` (`GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or
`models.credentials`). A binary built with `-tags llmfake` also accepts `plugins: [fake]` and
`fake/simulator`, `fake/judge` models that replay `TAMESU_LLMFAKE_SCRIPT`, to run without keys.

Global flags: `--config tamesu.yaml`, `-v/--verbose`, `--var KEY=VAL` (repeatable; ref vars for
`${VAR}` in secret refs and `{{vars.KEY}}`), `--env-file .env` (read through mamori's dotenv
provider when an `env:` secret or `${VAR}` is not set; the process environment is never mutated).

Exit codes: `0` passed, `1` a test failed, `2` config/schema/usage error (also an unresolvable
secret ref), `3` infrastructure error (a secret provider down, LiveKit or a model unreachable),
`4` budget exceeded.

## Debugging

- `validate` prints every problem; fix from the top, the line number points at the YAML key.
- `warning: targets.voice.url: env var LIVEKIT_URL is not set` means the suite is fine but the
  machine lacks the variable; `doctor` and the runner treat it as an error.
- `secret X is not declared`: add `X` under `secrets:` in the suite or `tamesu.yaml`.
- `tamesu run` exit 3 with `no simulator model is available` means `models.simulator` (or
  `models.default`) is unset or its plugin is not configured; `target type X is not available in
  this build` means the lite build lacks it (audio and phone targets need the full build).
- A judge failure prints the judge's reason under the test; the transcript's last turns follow.
- `doctor` says `fail secret X: not found in any of N ref(s)`: none of the chain's links had a
  value; `provider ... failed (unavailable)` is the backend, not your config (exit 3).
- `ref var ENV is not set`: a secret ref uses `${ENV}`; pass `--var ENV=staging` or add it to
  `vars:`.
- Values are never printed. If you need to confirm a secret resolved, look at `doctor`'s
  `(link i of n)` and the scheme.

## Go API

```go
suite, err := tamesu.LoadSuite("tests/support.yaml")   // validated; errors list file:line problems
```

`target.Target` / `target.Session` are what a target implements; `assert.Assertion` is what an
assertion implements; `metrics` names the built-in metrics.
