---
name: tamesu
description: Use when working with tamesu (試す), the Go agentic testing framework and CLI for web apps and AI agents, when writing or fixing a tamesu suite in YAML (targets of type browser, rest, grpc, livekit, sip, twilio (Twilio Media Streams without a phone number: url, parameters, from, to), realtime, pipecat, mcp (an MCP server over stdio (command) or streamable HTTP (url) whose tools a model drives: allow_tools, done_tool, call_timeout), a2a (an agent speaking the Agent2Agent protocol over JSON-RPC: url, headers, stream for message/stream, skill the agent card must offer, metadata, turn_timeout), vapi (a Vapi assistant over the vapi.websocket transport: api_key, assistant_id or assistant or squad_id, assistant_overrides, variables, sample_rate, idle, no phone number), retell (a Retell agent through a web call: api_key, agent_id, agent_version, variables, livekit_url, join_timeout), voiceflow (an agent on Voiceflow's Conversations API: api_key, project_id, environment, version draft or published, variables, user_id, reset, or version_id for the legacy Dialog Manager API) or model (a system prompt and tools tested directly against a model, with system / system_file, tools with canned results, done_tool, fallback models); personas, scenarios, steps, assertions such as judge, contains, not_contains, regex, not_regex, similar_to (embedding similarity), json_path, status, tool_called (with args or a JSON schema for the arguments), tool_not_called, tool_sequence, captured, golden, dtmf_received, call_ended_by, transferred_to, handoff (the agent handed the conversation to a person), http and command (the state of your backend after the conversation), grounded (every factual claim is supported by the retrieval context: a tool's results, a json_path over the responses or given text), evaluator, metric, turns, custom, on_turn, and the red-team packs pack: prompt_injection / pii_leakage / system_prompt_leakage or suite-defined packs), configuring tamesu.yaml (genkit plugins, models with fallback models and retry, an embedder, speech, sip, defaults, budgets, reporters, secrets via mamori refs such as env: aws-sm:// vault:// op://), running `tamesu init`, `tamesu validate`, `tamesu list`, `tamesu run` (filter, tags, target, shard i/n for CI sharding, fail-fast, parallel, repeat, load and load-duration for load mode, mode, headed, replay and update-cache (the caches: browser action plans, simulator turns, judge verdicts; cache.llm in tamesu.yaml), update-golden, update-visual, baseline and baseline-dir, out, reporter (console, json, junit with the file and line of each test, html, markdown, github with annotations and the job summary, slack with slack.webhook, sarif with the red-team findings as tamesu.sarif for GitHub's security tab), max-cost-usd, estimate to price the run from past usage, matrix KEY=v1,v2 to run and compare models or prompts), `tamesu doctor`, `tamesu lint` (weak tests: judge-only checks, untagged tests, duplicate goals, limits that never bind, unused declarations), `tamesu playbooks` (the red-team playbooks a suite's red_team block runs, with their OWASP LLM Top 10 categories and attacks), `tamesu coverage` (what a run exercised: targets, tags, tools and the declared tools never called, assertion kinds, stop reasons, attack tactics), `tamesu report` (rebuild the HTML report of a run directory), `tamesu merge` (combine the run directories of shards or repeated runs into one run, report and verdict, with --out), `tamesu history` (each test's pass rate, verdicts and p95 over the last runs under .tamesu/runs, flaky tests, with --runs-dir, --last, --filter, --flaky, --json), `tamesu baseline` / `tamesu baseline save` (keep a run as the baseline), `tamesu compare` (a run against the baseline: new failures, p95 latency and cost growth; exit 1 on a regression; also `tamesu run --baseline`), `tamesu record` / `tamesu record browser` (record a browser session as steps), `tamesu generate` (draft tests from a PRD or system prompt with --from, --target, --type, --count, --out), `tamesu import` (turn recorded conversations in JSON, JSON Lines or CSV, or LangSmith, Langfuse and Voiceflow exports with --format, into a suite that replays them: a scenario script of say steps and a golden assertion per conversation, with --target, --type, --out, --golden-dir, --golden-mode, --tags), `tamesu judge` / `tamesu judge calibrate` (run the judge over labelled transcripts and report the agreement per rubric, false passes and false fails; exit 1 under --min-agreement), `tamesu sip` / `tamesu sip setup` (LiveKit SIP trunks and dispatch rule for the phone target), `tamesu mcp` (serve tamesu to coding agents over the Model Context Protocol, stdio or --http with --http-token, with --cwd; run directories as tamesu://runs resources) or `tamesu version`, reading its exit codes (0 passed, 1 failed, 2 config error, 3 infrastructure error, 4 budget exceeded), running it in CI, reading its reports and run directories, debugging a failing validation or an unresolved secret, or answering what the tamesu CLI can do. Covers github.com/xavidop/tamesu.
---

# tamesu: your agents, tested like users would

tamesu (試す, Japanese for *to try, to test*) is a Go CLI and framework that runs
agentic tests against web apps and AI agents. A **persona** with a **scenario** plays the user;
the **target** is the system under test: `browser` (Chromium, driven by the browser agent on
`models.browser_agent`), `rest` (HTTP, SSE, websocket), `livekit` (voice over WebRTC, text and
audio), `sip` (phone calls through a LiveKit SIP service), `realtime` (the OpenAI Realtime
protocol, text and audio), `grpc` (an agent behind tamesu's turn API, `proto/tamesu/turn/v1`),
`pipecat` (a Pipecat bot's websocket transport, text and audio), `model`, `mcp`, `a2a` (an
agent speaking the Agent2Agent protocol: its card, `message/send` or `message/stream`, tasks
through input-required), `vapi` (a Vapi assistant called over its websocket transport, audio
only), `retell` (a Retell agent through a web call in its LiveKit room, audio only) or
`voiceflow` (an agent on Voiceflow's Conversations API: launch, text turns and its traces); **assertions** mix deterministic checks with LLM **judges**; every run
leaves a transcript, metrics and reports. Module: `github.com/xavidop/tamesu`. Docs:
https://xavidop.github.io/tamesu/docs/

## The model in one minute

- A **suite** is a YAML file: `suite:` (name), `tags:`, `vars:`, `secrets:`, `targets:` (by
  name) and `tests:`.
- A **target** has a `type`: `browser` (a URL, `browser: {engine, viewport, locale}`,
  `auth.storage_state`, `allowed_domains`; goals run through the browser agent on an
  accessibility tree with numbered refs, secrets are typed only inside the browser), `rest` (URL, method,
  headers, how a user turn is sent with `request.body` and how the agent turn is read back
  with JSONPaths in `response.text`, `response.tool_calls`, `response.done`; when the reply
  arrives somewhere else, `session.open` creates the conversation with captures as
  `{{session.NAME}}`, `response.poll` polls a second endpoint with `cursor`, `interval`, `idle`
  and `request.capture` as `{{sent.NAME}}`, and `response.listen` serves an API at
  `{{listen.url}}` with `addr`, `path`, `session`, `reply`, `idle`: Slack, Teams via Direct Line,
  Telegram and WhatsApp bots), `livekit` (server
  URL, `api_key`/`api_secret` from secrets, `room`, `dispatch.agent_name`,
  `agent_identity_prefix`, `mode: text|audio`, `data_topic`, `join_timeout`; text mode sends
  user turns on `lk.chat` and reads replies on `lk.transcription`, ending a turn on the agent's
  `lk.agent.state` or after 1.5 s idle; audio mode speaks with the TTS model and listens with
  the VAD and STT, `transcript_source: auto|agent|stt`) or `sip` (`direction: outbound|inbound`,
  `to`, `from`, `trunk_id`, `ring_timeout`, `max_call_duration`, an inbound `trigger` with
  `http` or `command`; the project's `sip:` block has `livekit_url`, `outbound_trunk_id`,
  `inbound_number`, `allowed_numbers` (required when `to` is a template) and `trunk` for
  `tamesu sip setup`), or `a2a` (`url` of the JSON-RPC endpoint, `headers`, `stream: true` for
  `message/stream`, `skill` the agent card must offer, `metadata` on every message,
  `turn_timeout`; the card is read from the URL's origin, `a2a_skills` / `a2a_tasks` /
  `a2a_requests` metrics, `card` and `task_<state>` events, a failed, canceled or rejected task
  ends the session, a completed one does not), `vapi` (`api_key` as a secret, exactly one of
  `assistant_id`, `assistant` (a transient assistant object) or `squad_id`, `assistant_overrides`,
  `variables` as variableValues, `metadata`, `sample_rate` default 16000, `idle`, `turn_timeout`,
  `transcript_source`; audio only through the speech models; events `call`, `greeting`,
  `status`, `ended_reason`, `user_interrupted`, `tool_call`, `hangup`) or `retell` (`api_key`,
  `agent_id`, `agent_version`, `variables` as retell_llm_dynamic_variables, `metadata`,
  `livekit_url`, `join_timeout`, `idle`, `turn_timeout`; the call is created with
  /v2/create-web-call and the room joined with its token; audio only; event `retell_call`), or
  `voiceflow` (`api_key` a personal or service token, `project_id`, `environment` default main,
  `version` draft or published, `variables` set before the launch, `user_id` template, `reset`
  to delete the state first, `base_url`; or `version_id` with a Dialog API key for the legacy
  Dialog Manager API; the launch's greeting is read with the first turn; text and speak traces are
  the words, choice / cardV2 / carousel are summarised as [buttons: ...] / [card: ...], an end
  trace ends the session, tool-call and live-agent-handoff traces are events `tool_call` and
  `live_agent_handoff`; metric `voiceflow_traces`; the raw traces are in result.json for json_path).
- A **test** names a target. Browser tests have `steps` (`goal:` agentic, `expect:` agentic
  assertion, `action:` one of `navigate click fill press wait screenshot eval_js assert_url
  assert_text mock a11y visual`; `browser.mocks` on the target answers requests, `visual`
  baselines live in `visual/` next to the suite and `--update-visual` rewrites them). Conversational tests have a `persona` (with `adversarial: true` for a red-team attacker that
  escalates tactics and records each as a `tactic` event, `tactics` for your own list) and a `scenario` with a `goal`,
  `max_turns`, `stop_when` and, for voice, a `script` of `wait_for` / `dtmf` / `say` steps. `languages: [en, es]`
  runs the test once per language (`<name> [es]`, tag `lang:es`, `{{vars.language}}`, the judge told the language).
- **Assertions** go under `assert:`. Exactly one kind per entry: `judge` (a rubric string, or
  `{ rubric, model, scale: binary|1-5, min }`), `contains`, `not_contains`, `regex`, `not_regex`,
  `similar_to` (a text, or `{ text, min, model }`: some agent turn means the same, by embedding
  similarity of at least `min`, default 0.8, with `models.embedder`),
  `json_path` (`{ path, equals }`, rest, a2a and voiceflow), `status` (rest only), `tool_called` /
  `tool_not_called` (a name, or `{ name, args }`; `tool_called` also takes `schema`, a JSON Schema
  every call's arguments must validate against), `tool_sequence` (`{ calls: [a, b], exact }`),
  `captured` (`{ name, equals | matches }`), `golden` (`{ file | turns, mode: exact | similar |
  judge, min }`, reference turns; `tamesu run --update-golden` rewrites the files), `dtmf_received` (`{ equals }`, sip and
  livekit audio), `call_ended_by` (sip: `agent caller timeout error no_answer busy`),
  `transferred_to` (`{ matches }`, sip), `handoff` (`true`, `false` or `{ within_turns, to, summary_contains, summary }`:
  the agent handed the conversation to a person as the target's `handoff: { tool | json_path | regex, to, summary }`
  signal says; a SIP transfer counts), `evaluator` (a Genkit evaluator name, or `{ name, min }`),
  `metric` (`{ name, lt|lte|gt|gte|eq }`), `turns` (`{ lte: 6 }`), `http` (`{ url, method, headers, body,
  status, json_path, contains, regex }`: the state of your backend after the conversation, templates see
  `{{captured.x}}` and `{{secrets.X}}`), `command` (`{ run, exit, contains, regex }`: a shell command's exit
  code and output after the conversation), `custom` (a Go-registered
  name). Add `on_turn: N` or `on_turn: last` to check one agent turn.
- **Reaching the agent**: every target but `browser` retries its connection (and, for `rest` and
  `grpc`, a request that never arrived or came back 502/503/504); `connect: { attempts, backoff,
  max_backoff }` on the target tunes it (default 3 attempts, 500ms doubling to 5s).
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
    persona: { name: Marta, style: "polite, slightly confused", language: es }
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
  `json_path` is rest, a2a and voiceflow only, `status` rest only; `call_ended_by`/`transferred_to` are sip only;
- `quarantine: { reason, until: YYYY-MM-DD }` marks a known flaky test: it runs and is reported
  (`QUAR`, JUnit `skipped`, a GitHub warning) but does not fail the run or count as a regression;
  `reason` is required, `until` is the last day and an expired one is a validation warning.
- `repeat` is 1 or more, `pass_threshold` is in (0, 1], `mode` is `text` or `audio` and only on
  livekit, sip or realtime tests; `vars` (per-test {{vars.x}}), `cases` / `cases_file`
  (parameterised: one test per case, named `<name> [<case>]`, tagged case:<name>); `hooks`
  (`before_all` / `after_all` / `before_each` / `after_each`, each `command` or `http`, on the
  suite or in tamesu.yaml); `defaults.assert` (assertions every test must pass, packs included,
  reported as defaults/<name>) and `defaults.slo` (metric maximums such as turn_latency_ms_p95: 3000,
  reported as slo/<metric>; unmeasured metrics pass); `capture` (`{ name, regex | json_path | extract }`: values from the agent's turns; `extract` is a plain-language description a model takes the value out with
  as `{{captured.<name>}}` in later script say steps, tool_results, hooks and assertions);
- every `{{secrets.X}}` must be declared; a `${VAR}` in a secret field is a warning;
- an unset `${VAR}` is a warning for `validate` (so CI without the variables can check suites)
  and an error for `doctor` (and the runner).

## Configuration (tamesu.yaml)

```yaml
version: 1
suites: ["tests/**/*.yaml"]          # what run/validate/list use when no paths are given
genkit: { plugins: [googlegenai, anthropic, openai, ollama, deepgram, elevenlabs, cartesia, soniox], dev_ui: false }
models:                               # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  browser_agent: googleai/gemini-2.5-pro
  simulator: openai/gpt-4.1-mini
  scenario_generator: googleai/gemini-2.5-pro
  embedder: googleai/text-embedding-004                                            # similar_to assertions
  fallback: { default: [openai/gpt-4.1-mini], judge: [googleai/gemini-2.5-pro] }   # tried in order when a job's model keeps failing
  retry: { attempts: 3, backoff: 500ms, max_backoff: 10s }                        # per model; 429, 5xx, timeouts, bad JSON
speech:
  stt: { model: deepgram/nova-3, language: en, fallback: [googleai/gemini-2.5-flash] }
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
`DEEPGRAM_API_KEY`, `ELEVENLABS_API_KEY`, `CARTESIA_API_KEY`, `SONIOX_API_KEY`) or from `models.credentials`.

## Commands

```text
tamesu init [dir] [--force]                 scaffold tamesu.yaml and examples/
tamesu validate [paths...] [--strict]       check tamesu.yaml and the suites; exit 2 on errors
tamesu lint [paths...] [--json] [--strict]  weak tests: no-assertions, judge-only, untagged, duplicate-goal, short-goal, vague-rubric, turns/latency limits that never bind, unused targets/packs/vars/secrets; --strict exits 1
tamesu list [paths...] [--json] [--filter re] [--tags a,b] [--target name]
tamesu run [paths...] [--filter re] [--tags a,b] [--target name] [--shard i/n] [--fail-fast] [--parallel N] [--repeat N | --load N [--load-duration D]]
           [--mode text|audio] [--headed] [--replay | --update-cache] [--out dir] [--reporter console,json,junit,html,markdown,github,slack,sarif] [--max-cost-usd X]
           [--env name] [--estimate] [--matrix KEY=v1,v2]   env: a named environment from environments: (its vars, secrets, an env label); estimate: price the selection from past runs and exit; matrix: run every combination of env / models.<job> / vars.<x> / targets.<t>.<field> values and compare
tamesu doctor [paths...]                    config, suites, every secret (which chain link answered), models, speech, Chromium, LiveKit, SIP
tamesu baseline save <run-dir> [--dir d]    keep a run's summary.json as the baseline (.tamesu/baseline)
tamesu compare <baseline-dir> <run-dir>     new failures, fixed tests, p95 latency / cost growth over the limits; exit 1 on regression
tamesu merge <run-dir>... [--out d]         one run directory, report and exit code from shards or repeated runs
tamesu history [--runs-dir d] [--last N] [--flaky] [--json]   pass rate, verdict dots and p95 per test over the last runs
tamesu coverage <run-dir> [--json]          what the run exercised: targets, tags, tools (and declared tools never called), assertion kinds, stop reasons, tactics, red-team playbooks
tamesu playbooks [name] [--json]            the red-team playbooks (prompt_injection, pii_extraction, system_prompt_leakage, policy_drift, human_impersonation, scope_hijack, unauthorized_actions, harmful_content) with OWASP ids; with a name, its attacks
tamesu report <run-dir>                     rebuild report.html (self-contained: transcripts, assertions, metrics, screenshots, recording, audio) from a run
tamesu record browser <url> [--out f]       open a window, record clicks/typing/navigation as action steps (passwords become {{secrets.PASSWORD}})
tamesu generate --from f --target t [--type rest|livekit|sip|browser] [--count N] [--out f]   draft tests from a PRD/system prompt; validates the draft
tamesu import <file> --target t [--type rest] [--out f] [--golden-dir d] [--golden-mode similar]   recorded conversations (json/jsonl/csv) → one test per conversation: script say steps + golden answers
tamesu judge calibrate <labels.yaml> [--min-agreement 0.8] [--out f]   judge labelled transcripts; agreement per rubric, false pass/fail; exit 1 under the minimum
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

## Breaking the agent's tools

`fuzz_tools` on a test (realtime, pipecat, model and mcp targets) replaces the named tools' answers for
that test: `lookup_order: error` (or `empty`, `null`, `malformed`, `hostile`, `huge`, `wrong_type`,
`{ mode: delay, delay: 8s }`, `{ mode: custom, result: ... }`); pair it with `pack: graceful_degradation`.
A `red_team:` block on a suite (`target`, `agent`, `playbooks`, `max_turns`, `assert`) adds one adversarial
test per attack of the built-in playbooks; `tamesu playbooks` lists them. A target's `handoff:` signal
(`tool` / `json_path` / `regex`, `to`, `summary`) plus the `handoff` assertion test escalation.
`environments:` in tamesu.yaml plus `tamesu run --env <name>` (or `--matrix env=a,b`) run against named deployments.
