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

## Handoff and escalation

Some conversations must end with a person: the angry customer, the request the agent is not
allowed to grant, the user who asks for a human. Others must not. Tell the target how your agent
signals a handoff, and assert on it:

```yaml
targets:
  api:
    type: rest
    url: ${AGENT_URL}
    handoff: { tool: transfer_to_human, to: department, summary: summary }   # or json_path / regex

tests:
  - name: angry customer reaches a person
    target: api
    persona: { name: Ray, style: furious, swears every turn }
    scenario: { goal: "Complain about a late delivery until a human takes over", max_turns: 6 }
    assert:
      - handoff: { within_turns: 3, to: "^support$", summary_contains: ["late"], summary: "The summary says what the customer is angry about" }
      - pack: discloses_ai
  - name: a simple question stays with the agent
    target: api
    scenario: { goal: "Ask how long shipping takes", max_turns: 3 }
    assert:
      - handoff: false
```

`handoff` on a target is one of `tool` (a tool call such as `transfer_to_human`), `json_path`
(a field of the raw response that is present and not false) or `regex` (words in the reply);
`to` and `summary` say where the destination and the summary live (a tool argument name or a
JSONPath; with `regex`, the first group is the destination). When the signal fires the
conversation ends with stop reason `handoff`, a `handoff` event records the turn, the
destination and the summary, and `handoff_turn` is a metric. A SIP transfer (`transferred_to`)
counts as a handoff to its target.

| Assertion | Passes when |
|---|---|
| `handoff: true` | the agent handed the conversation to a person (a handoff event or a call transfer). |
| `handoff: false` | it did not: no over-escalation. |
| `handoff: { within_turns, to, summary_contains, summary }` | it did, on or before agent turn `within_turns`, to a destination matching `to`, with a summary that contains every `summary_contains` text and that the judge accepts against the `summary` rubric. |

Pair a handoff test with `pack: discloses_ai`: an agent that escalates well but claims to be a
person when asked still fails.

## Your backend's state

A conversation that sounds right can still have done the wrong thing. These two run after the
conversation ends and look at your own systems; their templates see `{{vars.x}}`,
`{{captured.x}}` (a value [captured](/docs/concepts/#capturing-values-across-turns) from the agent's turns,
such as an order id) and `{{secrets.X}}`.

| Assertion | Passes when |
|---|---|
| `http: { url, method, headers, body, status, json_path, contains, regex, timeout }` | the request returns the `status` (any code under 400 when omitted) and the body satisfies every check given: `json_path: { path, equals }` exists in the JSON body (and equals the value), `contains` is in the body, `regex` matches it. `method` is `GET` by default; a mapping `body` is sent as JSON. |
| `command: { run, exit, contains, regex, timeout }` | the shell command (`sh -c`) exits with `exit` (`0` when omitted) and its output contains `contains` and matches `regex`. |

```yaml
tests:
  - name: refund goes through
    target: api
    capture:
      - { name: order_id, regex: "(ORD-[0-9]+)" }
    scenario: { goal: "Get a refund for the shoes that arrived damaged", max_turns: 6 }
    assert:
      - judge: "The agent confirms the refund and gives a timeline"
      - http:
          url: "{{vars.admin_url}}/orders/{{captured.order_id}}"
          headers: { Authorization: "Bearer {{secrets.ADMIN_TOKEN}}" }
          json_path: { path: "$.status", equals: refunded }
      - command: { run: "./scripts/check-ledger.sh {{captured.order_id}}", contains: credited }
```

The result quotes the status and the first lines of the body or output as evidence, so a failing
check shows what the backend actually held. Both default to a 30-second timeout; `on_turn` does
not apply, since the state is read once, after the conversation.

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
