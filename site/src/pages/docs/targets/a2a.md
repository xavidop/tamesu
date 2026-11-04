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

## Your agent's remote agents

An orchestrator rarely works alone: it delegates to other agents over A2A. To test the
orchestrator without those agents, declare them under `remote_agents` and tamesu runs them for
the duration of each run: a card with a name and skills, and scripted replies. Point the agent
under test at `{{remote.<name>.url}}` (its card is at `/.well-known/agent-card.json`, its
JSON-RPC endpoint at the URL or `/a2a`), or give the agent a fixed `addr` when it is configured
by hand. Every message the orchestrator sends is recorded as a **delegation**, with the reply,
and `delegated_to` asserts on it.

```yaml
remote_agents:
  billing:
    skills: [refund, invoice-lookup]
    reply: "I can help with refunds and invoices."           # the default answer
    replies:                                                 # the first match wins
      - { when: "(?i)refund", say: "Refund of 42 euros issued, reference RF-889", data: { reference: RF-889 } }
      - { when: "(?i)cancel", say: "Which order should I cancel?", state: input-required }
  shipping:
    addr: 127.0.0.1:8096                                     # the orchestrator's config names this port
    skills: [track]
    reply: "ORD-1001 is with the carrier and arrives tomorrow."
targets:
  orchestrator:
    type: mcp                                                # or any target the orchestrator runs behind
    command: ./bin/orchestrator
    env: { BILLING_AGENT_URL: "{{remote.billing.url}}", SHIPPING_AGENT_URL: "http://127.0.0.1:8096" }
tests:
  - name: refund goes to billing
    target: orchestrator
    persona: { name: Marta, style: direct }
    scenario: { goal: "Get a refund for order ORD-1001", max_turns: 4 }
    assert:
      - delegated_to: { agent: billing, contains: "ORD-1001" }   # a name, or agent / contains / regex
      - tool_not_called: shipping                                # a delegation is a tool call named after the agent
      - judge: "The agent reports the refund reference RF-889 to the user"
      - metric: { name: delegations, lte: 2 }
  - name: billing is down
    target: orchestrator
    fuzz_tools: { billing: error }                               # the fuzz modes apply to remote agents
    persona: { name: Marta, style: direct }
    scenario: { goal: "Get a refund for order ORD-1001", max_turns: 4 }
    assert:
      - delegated_to: billing
      - pack: graceful_degradation
```

Delegations are tool calls of kind `delegation`, named after the agent, with the message as
`args.message` and the reply as the result, so `tool_called`, `tool_not_called`,
`tool_sequence` and `tamesu coverage` see them too; each is also a `delegated` event, and
`delegations` counts them. `fuzz_tools` naming a remote agent replaces its replies: `error`
fails the task, `empty` answers nothing, `hostile` answers an injection, `delay` holds the
answer, `custom` answers your text or data. The remote agents start with every run, so with
`--parallel` give each test's agents no `addr` (a free port each) or run such tests alone.

### Recording a real remote agent

With `forward`, a remote agent becomes a **recording proxy**: every JSON-RPC request the
orchestrator sends it goes on to the real agent, the answer (or the event stream) comes back
unchanged, and both directions are recorded as delegations. `headers` adds the real agent's
credentials. `fuzz_tools` still applies and then answers locally instead of forwarding, so a
real integration can be tested with the real agent and with that agent failing.

```yaml
remote_agents:
  billing:
    forward: https://billing.internal.example.com/a2a
    headers: { Authorization: "Bearer {{secrets.BILLING_TOKEN}}" }
```

## Two agents talking

A test can put another agent in the user's seat instead of a persona: `counterpart` names a
target of the suite (an `a2a` agent, a `model` prompt, a `rest` endpoint, anything
conversational) and tamesu brokers the exchange. The counterpart's `opening` is the first user
turn (without one, the counterpart answers `scenario.goal` first); each agent turn is then sent
to the counterpart and its answer is the next user turn, until one of them stops or
`max_turns` is reached. The transcript reads user and agent as usual, the judges grade it, and
the run records a `counterpart` event.

```yaml
targets:
  buyer: { type: a2a, url: https://agents.example.com/buyer }
  supplier: { type: a2a, url: https://agents.example.com/supplier }
tests:
  - name: supplier negotiates with buyer
    target: buyer
    counterpart:
      target: supplier
      opening: "We can offer 500 units at 12 euros each, delivery in 3 weeks"
    scenario: { max_turns: 8 }
    assert:
      - judge: "The buyer never accepts a unit price above 11 euros"
      - judge: "Both agents reach a clear agreed or declined outcome"
      - turns: { lte: 8 }
```

Two agents can be polite forever, so `max_turns` and a `turns` assertion are the brakes; a
`model` target with a short system prompt makes a cheap, steerable counterpart.

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
