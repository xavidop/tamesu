---
layout: ../../layouts/DocsLayout.astro
title: Concepts
---

# Concepts

![A persona, a target and a checklist joined by one line that ends in a check mark](/img/concepts.jpg)

tamesu has a small vocabulary that is the same for every kind of target. This page defines it,
then shows how a conversational test flows and how a result is graded.

## Vocabulary

| Concept | Description |
|---|---|
| **Suite** | A YAML file (or Go package) containing targets, defaults and tests. |
| **Target** | The system under test: `browser`, `rest`, `grpc`, `livekit`, `sip`, `realtime` or `pipecat`. |
| **Test** | One scenario run against one target. |
| **Step** | For browser tests: a natural-language `goal`, a deterministic `action`, or an `expect`. |
| **Persona** | The simulated user for agent tests: identity, goal, style, voice, behaviours (interrupts, silence, noise). |
| **Scenario** | What the persona is trying to achieve and when to stop. |
| **Assertion** | A check evaluated during or after a run: deterministic (`contains`, `regex`, `status`, `json_path`, `tool_called`, `metric`) or LLM-based (`judge`). |
| **Run** | One execution of a test. A test may run N times (`repeat`) to measure variance. |
| **Artifact** | Anything captured: transcript, audio (WAV), screenshots, trace, HAR, Genkit trace IDs. |

## Targets

A target is declared once under `targets:` and named by tests. The `type` decides which keys
apply; `tamesu validate` warns when a key belongs to another type.

- **`browser`**: a URL, browser options (engine, viewport, locale), an optional pre-authenticated
  storage state and `allowed_domains`. Tests are a list of steps: `goal` (the browser agent
  plans and acts until the goal is met), `expect` (an agentic assertion against the page) and
  `action` (deterministic: `navigate`, `click`, `fill`, `press`, `wait`, `screenshot`,
  `eval_js`, `assert_url`, `assert_text`, `mock`, `a11y`, `visual`).
- **`rest`**: a URL and method, headers, how a user turn is sent (`request.body`, with
  `{{session}}` and `{{message}}`), how the agent turn is read back (JSONPaths in
  `response.text`, `response.tool_calls`, `response.done`), `streaming: none|sse|websocket` and a
  session id template.
- **`livekit`**: the LiveKit server URL, `api_key`/`api_secret` (as `{{secrets.X}}`), a `room`
  template, an optional agent dispatch, how to recognise the agent participant, `mode: text|audio`
  and the text-mode data topic.
- **`sip`**: `direction: outbound` (tamesu calls the agent at `to`, presenting `from`) or
  `inbound` (a `trigger` makes the agent call `sip.inbound_number`), the trunk, ring timeout and
  maximum call duration. A SIP call is bridged into a LiveKit room, so everything audio mode does
  applies.

The full key list is in [Suites](/docs/suites/).

## Personas and scenarios

A conversational test (rest, grpc, livekit, sip, realtime, pipecat) is driven by a **persona** and a **scenario**:

```yaml
persona:
  name: Alice
  style: impatient
  language: en-GB
  voice: en-GB-female                 # TTS voice for audio mode
  behaviors:
    interrupt_probability: 0.3        # talk over the agent sometimes
    silence_after_turn_ms: [300, 1500]
    background_noise: cafe
scenario:
  goal: "You booked a room for Friday; move it to Saturday"
  max_turns: 12
  stop_when: "the agent confirmed the new date"
  script:                             # sip and livekit audio: deterministic steps mixed in
    - wait_for: "press 2 for billing"
    - dtmf: "2"
    - say: "Hi, my last invoice is higher than usual"
```

The simulated user, given the persona, the scenario and the conversation so far, produces the
next utterance (or a DTMF digit, or decides to hang up or stop). Its model is
`models.simulator`; a `seed` per test keeps it as repeatable as the provider allows.

## Suite-wide assertions and SLOs

Rules that every test must meet live once, under `defaults` in `tamesu.yaml` or in a suite,
instead of in every test. `defaults.assert` is a list of assertions (packs included) that runs
before each test's own, reported as `defaults/<assertion>`; `defaults.slo` caps metrics by name,
reported as `slo/<metric>`, and a metric a test does not measure, such as an audio metric on a
text test, passes. A suite's entries run after the project's and its SLOs override them.

```yaml
defaults:                                   # tamesu.yaml, or a suite
  assert:
    - pack: pii_leakage
    - not_contains: "as an AI language model"
  slo:
    turn_latency_ms_p95: 3000
    time_to_first_token_ms_p95: 1500
    llm_cost_usd: 0.05
```

## Hooks

`hooks` in the project or the suite run commands or HTTP requests before and after the whole
suite (`before_all`, `after_all`) and around every run (`before_each`, `after_each`): seeding,
resets and cleanup live next to the tests. See [Configuration](/docs/configuration/#hooks-setup-and-teardown).

## Parameterised tests

A test with `cases` runs once per case, each case giving values to `{{vars.<key>}}` in the goal,
the stop condition, the target's request or anywhere else a template is accepted. Each case
becomes a test of its own, named `<name> [<case>]` and tagged `case:<name>`, so `--filter`,
`--tags`, repeats and reports treat it like any other test.

```yaml
tests:
  - name: return window
    target: api
    vars: { lang: en }                          # this test's values, under the cases
    cases:
      - { name: late, vars: { days: "40", expect: "not possible" } }
      - { name: early, vars: { days: "10", expect: "can return" } }
    persona: { name: Marta, language: "{{vars.lang}}" }
    scenario: { goal: "Ask whether shoes bought {{vars.days}} days ago can be returned" }
    assert:
      - contains: "{{vars.expect}}"
```

`cases_file: cases.csv` (a header row; a `name` column names the case) or `cases_file:
cases.json` (an array of objects; a `name` key names the case) reads the cases from a file next
to the suite, so a spreadsheet of questions becomes a suite.

## Capturing values across turns

`capture` takes values from the agent's turns as they arrive, by a regular expression on the
agent's text (the first group, else the whole match) or a JSONPath on the turn's raw response.
The first turn that matches sets the value; from then on it is `{{captured.<name>}}` in later
script `say` steps, in `tool_results`, in hooks and in assertions, and it is written to
`result.json`.

```yaml
tests:
  - name: order status
    target: api
    capture:
      - { name: order_id, regex: "order (ORD-[0-9]+)" }
      - { name: eta, json_path: "$.eta" }
    scenario:
      goal: "Place an order for the blue sneakers, then ask when it arrives"
      script:
        - say: "I'd like the blue sneakers in a 42"
        - say: "What is the status of order {{captured.order_id}}?"
    assert:
      - captured: { name: order_id, matches: "^ORD-" }
      - contains: "{{captured.eta}}"
```

## How a conversational test flows

1. tamesu opens a session on the target.
2. The persona loop runs: the simulator produces the next user message from persona, scenario and
   history; the target sends it and waits for the agent's turn; the turn is appended to the
   transcript; `stop_when`, `max_turns` and the agent's own end-of-session are checked.
3. Metrics are collected throughout. Timestamps come from the transport layer, never from a model.
4. The session closes and the artifacts are saved under the run directory.
5. Assertions are evaluated. Judges get the transcript, the tool calls and the metrics as
   structured input and answer with `pass`, `score`, `reason` and `evidence`.
6. The result is aggregated across `repeat` runs and compared with `pass_threshold`.

## Assertions

Every assertion is one key under `assert:`; a test may have any number, and `on_turn: N` or
`on_turn: last` scopes one to a single agent turn.

| Assertion | Targets | Example |
|---|---|---|
| `judge` | all | `judge: "..."` or `judge: { rubric: "...", model: ..., scale: binary\|1-5, min: 4 }` |
| `contains` / `not_contains` | all | on the full agent transcript |
| `regex` | all | `regex: "order #\\d{6}"` |
| `not_regex` | all | `not_regex: "[\\w.]+@[\\w.]+"` |
| `json_path` | rest | `{ path: $.status, equals: ok }` |
| `status` | rest | HTTP status of every turn |
| `tool_called` / `tool_not_called` | rest, grpc, livekit, realtime, pipecat | `tool_called: { name: reschedule_booking, args: { day: saturday } }` |
| `tool_sequence` | rest, grpc, livekit, realtime, pipecat | `tool_sequence: { calls: [lookup_order, refund] }` |
| `dtmf_received` | sip, livekit audio | `{ equals: "1234#" }` |
| `call_ended_by` | sip | `[agent, caller]`, also `timeout`, `error`, `no_answer`, `busy` |
| `transferred_to` | sip | `{ matches: "^sip:billing@" }` |
| `evaluator` | all | any registered Genkit evaluator: `{ evaluator: genkitEval/faithfulness, min: 0.8 }` |
| `metric` | all | `{ name: time_to_first_audio_ms, lt: 1500 }` |
| `turns` | conversational | `{ lte: 6 }` |
| `grounded` | all | `{ context: { tool: search_docs } }`: every factual claim is supported by the retrieval context ([grounding](/docs/assertions/#grounding)) |
| `handoff` | conversational | `true`, `false`, or `{ within_turns: 3, to: "^billing$", summary_contains: [...], summary: "rubric" }`: the agent handed the conversation to a person, as the target's `handoff` signal says ([details](/docs/assertions/#handoff-and-escalation)) |
| `http` | all | `{ url: "{{vars.admin_url}}/orders/{{captured.order_id}}", json_path: { path: "$.status", equals: refunded } }`: the state of your backend after the conversation ([details](/docs/assertions/#your-backends-state)) |
| `command` | all | `{ run: "./scripts/check.sh {{captured.order_id}}", contains: refunded }`: a shell command's exit code and output after the conversation |
| `custom` | all | a Go-registered assertion by name |
| `pack` | all | `pack: prompt_injection`: a reusable set of assertions, built-in or defined under `packs:` ([red-team packs](/docs/red-team/)) |

Judges return structured output only (`pass`, `score`, `reason`, `evidence[]`), run at
temperature 0 when the provider supports it, and their prompt version is recorded with every
result so judge drift is traceable.

## Metrics

| Metric | Targets | Definition |
|---|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | rest, grpc, livekit, sip, realtime, pipecat | user turn sent → agent turn complete |
| `time_to_first_token_ms` | rest (sse, websocket), grpc, realtime, pipecat | user turn sent → first delta |
| `time_to_first_audio_ms` | livekit audio, sip, realtime audio, pipecat audio | user speech end (VAD, or the audio commit) → agent speech start |
| `interruption_stop_ms` | livekit audio, sip | user starts talking over the agent → agent audio stops |
| `talk_over_ratio` | livekit audio, sip | overlapping speech time / total agent speech time |
| `silence_gap_max_ms` | livekit audio, sip | longest dead air while a reply was expected |
| `call_setup_ms` | sip | dial (or trigger, inbound) → call active |
| `ring_ms` | sip | ringing → answered |
| `greeting_ms` | sip | call active → agent's first speech |
| `call_duration_ms` | sip | active → hangup |
| `stt_word_error_rate` | livekit audio, sip | the agent's transcript of the caller vs. the text tamesu synthesised |
| `turns` | conversational | number of agent turns |
| `goal_steps` | browser | agent steps used per goal |
| `llm_cost_usd`, `llm_tokens` | all | spend of tamesu's own model calls |

Metric names follow the pattern `name` or `name_p95`.

## Runs, repeats and exit codes

A test runs `repeat` times (default 1); the fraction of runs that must pass is `pass_threshold`
(default 1.0). `retries` re-runs a test whose infrastructure failed, never one whose assertions
failed; before it comes to that, every target retries [reaching the agent](/docs/configuration/#reaching-the-agent)
on its own. Every run gets its own directory with `transcript.json`, `result.json` and the artifacts
of its target.

| Code | Meaning |
|---|---|
| `0` | all tests passed |
| `1` | one or more tests failed |
| `2` | config or schema error |
| `3` | infrastructure error (browser could not start, LiveKit unreachable, model error after retries, secret provider down) |
| `4` | budget exceeded |

### Quarantining a flaky test

A test that fails for reasons you already know about can be quarantined instead of deleted or
skipped. It still runs on every invocation and its result is still written and reported, but a
failure neither fails the run nor counts as a regression against the baseline:

```yaml
tests:
  - name: refund status
    target: api
    quarantine: { reason: "SUP-412 flaky since the payments migration", until: 2026-12-31 }
    scenario: { goal: "Ask where the refund for order 1234 is", max_turns: 3 }
    assert:
      - judge: "The agent gives a refund date or asks for the order"
```

`reason` is required, so a quarantine always points at the ticket that tracks it. `until` is
the last day of the quarantine; from the next day the test counts again, and `tamesu validate`
warns that it expired. Without `until`, the quarantine lasts until you remove it. A quarantined
test that passes just passes. The console prints `QUAR` instead of `FAIL`, the summary line and
every report count quarantined tests apart from failures, the JUnit file reports them as skipped
with the failure in the message, and the GitHub reporter annotates them as warnings.
