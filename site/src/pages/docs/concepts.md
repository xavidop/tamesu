---
layout: ../../layouts/DocsLayout.astro
title: Concepts
---

# Concepts

tamesu has a small vocabulary that is the same for every kind of target. This page defines it,
then shows how a conversational test flows and how a result is graded.

## Vocabulary

| Concept | Description |
|---|---|
| **Suite** | A YAML file (or Go package) containing targets, defaults and tests. |
| **Target** | The system under test: `browser`, `rest`, `livekit` or `sip`. |
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
  `eval_js`, `assert_url`, `assert_text`).
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

A conversational test (rest, livekit, sip, realtime) is driven by a **persona** and a **scenario**:

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

The simulated user is a Genkit flow that, given the persona, the scenario and the conversation so
far, produces the next utterance (or a DTMF digit, or decides to hang up or stop). Its model is
`models.simulator`; a `seed` per test keeps it as repeatable as the provider allows.

## How a conversational test flows

1. The runner resolves the target and opens a session.
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
| `tool_called` / `tool_not_called` | rest, livekit, realtime | `tool_called: { name: reschedule_booking, args: { day: saturday } }` |
| `dtmf_received` | sip, livekit audio | `{ equals: "1234#" }` |
| `call_ended_by` | sip | `[agent, caller]`, also `timeout`, `error`, `no_answer`, `busy` |
| `transferred_to` | sip | `{ matches: "^sip:billing@" }` |
| `evaluator` | all | any registered Genkit evaluator: `{ evaluator: genkitEval/faithfulness, min: 0.8 }` |
| `metric` | all | `{ name: time_to_first_audio_ms, lt: 1500 }` |
| `turns` | conversational | `{ lte: 6 }` |
| `custom` | all | a Go-registered assertion by name |
| `pack` | all | `pack: prompt_injection`: a reusable set of assertions, built-in or defined under `packs:` ([red-team packs](/docs/red-team/)) |

Judges return structured output only (`pass`, `score`, `reason`, `evidence[]`), run at
temperature 0 when the provider supports it, and their prompt version is recorded with every
result so judge drift is traceable.

## Metrics

| Metric | Targets | Definition |
|---|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | rest, livekit, sip, realtime | user turn sent → agent turn complete |
| `time_to_first_token_ms` | rest (sse, websocket), realtime | user turn sent → first delta |
| `time_to_first_audio_ms` | livekit audio, sip, realtime audio | user speech end (VAD, or the audio commit) → agent speech start |
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
failed. Every run gets its own directory with `transcript.json`, `result.json` and the artifacts
of its target.

| Code | Meaning |
|---|---|
| `0` | all tests passed |
| `1` | one or more tests failed |
| `2` | config or schema error |
| `3` | infrastructure error (browser could not start, LiveKit unreachable, model error after retries, secret provider down) |
| `4` | budget exceeded |
