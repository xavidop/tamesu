---
layout: ../../layouts/DocsLayout.astro
title: Assertions
---

# Assertions

An assertion is one check over a finished run. Each entry under `assert:` is exactly one kind;
a test may have any number. Deterministic assertions read the transcript, the responses, the
tool calls, the metrics and the call timeline; `judge` and `evaluator` ask a model (see
[Judges](/docs/judges/)). Every result carries a one-line reason that the console, `junit.xml`
and `result.json` show, so a failure reads like a code review comment.

## The transcript

| Assertion | Passes when |
|---|---|
| `contains: text` | some agent turn contains `text` (case-sensitive). |
| `not_contains: text` | no agent turn contains `text`. |
| `regex: pattern` | some agent turn matches the Go regular expression. |
| `not_regex: pattern` | no agent turn matches the Go regular expression (the matched text is the evidence when one does). |
| `similar_to: "text"` | some agent turn means the same as `text`: the cosine similarity of their embeddings is at least `min` (0.8). `similar_to: { text, min: 0.7, model }` sets the threshold and the embedding model; the default model is `models.embedder`, or the usual embedder of your default model's provider (Gemini `text-embedding-004`, OpenAI `text-embedding-3-small`). The closest turn and its score are the evidence either way, so a threshold is easy to tune. Use it where `contains` is too literal: the answer is right but worded its own way. |
| `turns: { lte: 6 }` | the number of agent turns satisfies the one comparator (`lt`, `lte`, `gt`, `gte`, `eq`). |

## Responses (rest)

| Assertion | Passes when |
|---|---|
| `status: 200` | every turn's HTTP status is the code. |
| `json_path: { path: $.status, equals: ok }` | the path exists in every turn's response and, with `equals`, has that value (numbers and strings compared after JSON normalisation). |

## Tool calls

| Assertion | Passes when |
|---|---|
| `tool_called: name` | the agent called the tool at least once. |
| `tool_called: { name: reschedule_booking, args: { day: saturday } }` | the agent called it with those argument values (a subset of the call's arguments). |
| `tool_called: { name: reschedule_booking, schema: { type: object, required: [day], properties: { day: { enum: [saturday, sunday] } } } }` | the agent called it, and the arguments of **every** call to it validate against the JSON Schema (draft 2020-12): a call the agent made with a wrong type, a missing field or a value outside an enum fails the assertion, with the first violation as the reason. `args` and `schema` combine. `tamesu validate` checks the schema itself. |
| `tool_not_called: name` (or with `args`) | the agent never called it (with those arguments). |
| `tool_sequence: { calls: [lookup_order, refund], exact: false }` | the agent called those tools in that order: as a subsequence of its calls, or exactly that list with `exact`. |
| `captured: { name: order_id, matches: "^ORD-" }` (or `equals`) | a value the test captured from the agent's turns exists and matches; see [capturing values](/docs/concepts/#capturing-values-across-turns). |
| `golden: { file: golden/refund.json, mode: similar, min: 0.8 }` (or `turns: [...]`) | the agent's turns, in order, against reference turns: `exact` (normalised text), `similar` (word overlap of at least `min`, the default) or `judge` (the judge decides whether each turn means the same). A missing file is written from the run; `tamesu run --update-golden` rewrites them all. |

Tool calls come from the target: the REST target's `response.tool_calls` path, the LiveKit
agent's attributes and data messages.

## Metrics

```yaml
- metric: { name: turn_latency_ms_p95, lt: 4000 }
- metric: { name: time_to_first_audio_ms, lt: 1500 }
```

Exactly one comparator (`lt`, `lte`, `gt`, `gte`, `eq`). A metric the run did not collect fails
with the list of the ones it did. See [Metrics](/docs/metrics/).

## Phone calls (sip)

| Assertion | Passes when |
|---|---|
| `dtmf_received: { equals: "1234#" }` | the digits the agent sent, concatenated, equal the value (sip and livekit audio). |
| `call_ended_by: [agent, caller]` | the call ended for one of the listed reasons: `agent`, `caller`, `timeout`, `error`, `no_answer`, `busy`. |
| `transferred_to: { matches: "^sip:billing@" }` | the agent transferred the call (a SIP REFER) to a matching target. |

These read the call timeline the sip and livekit targets record.

## Models

| Assertion | Passes when |
|---|---|
| `judge: "rubric"` | the judge model says the rubric holds. `judge: { rubric, model, scale: binary\|1-5, min }` picks the model and the scale. |
| `evaluator: genkitEval/regex` or `{ name, min }` | a registered Genkit evaluator scores the run at least `min` (or reports PASS). |

## Custom

`custom: name` runs an assertion registered from Go with `assert.Register`; the Go API takes
`assert.Assertion` values directly. See [Go API](/docs/go-api/).

## Packs

| Assertion | Passes when |
|---|---|
| `pack: name` | every assertion of the pack passes; the entry expands when the suite loads and each result reads `name/<assertion>`. Built-in: `prompt_injection`, `pii_leakage`, `system_prompt_leakage`; a suite defines or overrides packs under `packs:`. See [Red-team packs](/docs/red-team/). |

## One turn only

`on_turn: N` (1-based) or `on_turn: last` evaluates the assertion on a single agent turn: its
text, its response and its status.

```yaml
- contains: "anything else"
  on_turn: last
- json_path: { path: $.end_session, equals: true }
  on_turn: last
```

## How a result reads

```text
FAIL   support-agent / refund policy question  2.1s
    x judge "The agent states the 30-day return policy and does not promise an exception": the agent said a 40-day return "should be fine", which promises an exception
    x metric turn_latency_ms_p95 < 4000: turn_latency_ms_p95 = 5120, want < 4000
    last turns:
      user:  So I can still return them?
      agent: Sure, 40 days should be fine, I'll make an exception.
```

`result.json` holds the same, plus the judge's evidence quotes, its trace id and the prompt
version.
