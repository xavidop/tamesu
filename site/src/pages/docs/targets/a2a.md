---
layout: ../../../layouts/DocsLayout.astro
title: A2A target
---

# A2A target

![Two robots handing a letter to each other across a small bridge between their desks, each holding a card of icons, while a person with a clipboard watches](/img/target-a2a.jpg)

The `a2a` target tests an agent that speaks the **Agent2Agent protocol**: JSON-RPC over HTTP
with an agent card that lists its skills, `message/send` for one reply per request or
`message/stream` for server-sent events while the agent works, and tasks that move through
`submitted`, `working`, `input-required` and a final state. The agent is the thing under test:
each user turn is one message, and the agent's turn is what the task answered.

```yaml
targets:
  planner:
    type: a2a
    url: https://agents.example.com/planner          # the JSON-RPC endpoint; the card is read from its origin
    headers: { Authorization: "Bearer {{secrets.A2A_TOKEN}}" }
    stream: true                                     # message/stream (SSE); falls back when the card says no
    skill: trip-planning                             # the card must offer it, or the session fails to start
    metadata: { tenant: acme }                       # sent on every message
    turn_timeout: 60s
tests:
  - name: plan a weekend
    target: planner
    persona: { name: Marta, style: "decisive, short messages" }
    scenario: { goal: "Plan two days in Lisbon in May for two people on a 600 euro budget", max_turns: 6 }
    assert:
      - judge: "The plan names places, days and a cost that stays within the budget"
      - json_path: { path: "$.status.state", equals: completed }
        on_turn: last
      - metric: { name: a2a_tasks, lte: 2 }
```

## How a conversation runs

1. tamesu reads the **agent card** at `/.well-known/agent-card.json` (then `agent.json`) on the
   URL's origin, and under the URL itself. The card's name, version and skill ids are recorded as a
   `card` event, `a2a_skills` counts them, and `skill` is checked against them. A missing card is a
   `card_missing` event, not an error, unless `skill` needs it.
2. Each user turn goes out as a `message` with one text part and a fresh `messageId`. The first
   message of a session opens a task; the agent's `contextId` is kept for the whole session.
3. The agent's turn is read from the result. For a **task**: the status message, then the text
   of the artifacts not read before and, when both are empty, the agent's messages in the history
   after ours. Data parts are rendered as JSON so a judge can read them. For a plain **message**
   result: its text parts.
4. A task in `input-required` (or `auth-required`) is **continued**: the next user turn carries
   its `taskId`. A `completed` task is followed by a **new task in the same context**, which is
   how a conversation goes on; it does not end the session. A `failed`, `canceled` or `rejected`
   task ends the session. Every state the agent reports is a `task_<state>` event.
5. With `message/send`, a task the agent left `working` is read with `tasks/get` every 500 ms
   until it settles, within `turn_timeout`. With `message/stream`, events are read until the final
   status update; `time_to_first_token_ms` is the first event with text.
6. Closing the session cancels a task still waiting for input with `tasks/cancel`.

Every result or streamed event of a turn is in `result.json`, so `json_path` reaches task states
(`$.status.state`), artifacts (`$.artifacts[*].name`) and data parts
(`$.artifacts[*].parts[?(@.kind == 'data')].data`), and streamed turns are a list of events. A
JSON-RPC error fails the turn with its code and message.

## Options

| Field | Meaning |
|---|---|
| `url` | The agent's JSON-RPC endpoint. The card is looked for at the URL's origin and under the URL. |
| `headers` | Request headers (`Authorization: "Bearer {{secrets.X}}"`), also sent when reading the card. |
| `stream` | Use `message/stream` (server-sent events) instead of `message/send`. When the card says the agent does not stream, `message/send` is used and a `no_streaming` event recorded. |
| `skill` | A skill id or name the card must offer; the session fails to start otherwise, naming the skills it has. |
| `metadata` | A mapping sent as the `metadata` of every message. |
| `turn_timeout` | How long one turn may take, including the wait for a working task (default `60s`). |
| `connect` | How a request that never reached the agent is retried, as for every target. |

## Metrics and events

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | the message was sent → the agent's turn is complete |
| `time_to_first_token_ms` | streaming: the message was sent → the first event with text |
| `a2a_tasks` | tasks the session opened |
| `a2a_requests` | JSON-RPC requests of the session (sends, polls, cancels) |
| `a2a_skills` | skills on the agent card |

Events: `card`, `card_missing`, `no_streaming`, `task_submitted`, `task_working`,
`task_input_required`, `task_completed`, `task_failed`, `task_canceled`, `task_rejected`.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except the voice ones and the tool
ones (A2A has no tool calls): `judge`, `contains`, `regex`, `similar_to`, `grounded` with a
`text` or `json_path` context, `json_path` over the task, `captured`, `golden`, `metric`,
`turns`, `http` and `command`, and the packs.

## Example

[`examples/a2a-agent`](https://github.com/xavidop/tamesu/tree/main/examples/a2a-agent) is a
small shop agent (order status and returns, with streaming and an input-required turn) and the
suite that tests it; `make integration` builds it and runs the suite with the fake models.
