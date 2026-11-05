---
layout: ../../../layouts/DocsLayout.astro
title: A2A target
---

# A2A target

![Two robots handing a letter to each other across a small bridge between their desks, each holding a card of icons, while a person with a clipboard watches](/img/target-a2a.jpg)

The `a2a` target tests an agent that speaks the **Agent2Agent protocol**: an agent card that
lists its skills, `message/send` for one reply per request or `message/stream` for server-sent
events while the agent works, tasks that move through `submitted`, `working`, `input-required`
and a final state, push notifications, and any of the protocol's three bindings (JSON-RPC,
HTTP+JSON, gRPC). The agent is the thing under test: each user turn is one message, and the
agent's turn is what the task answered.

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
  planner-grpc:
    type: a2a
    url: grpcs://agents.example.com:443              # the gRPC binding; the card comes from GetAgentCard
    headers: { Authorization: "Bearer {{secrets.A2A_TOKEN}}" }
    push: true                                       # task updates by push notification instead of polling
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
   URL's origin, and under the URL itself; a `grpc://` URL gets it with `GetAgentCard`. The card's
   name, version and skill ids are recorded as a `card` event, `a2a_skills` counts them, and `skill`
   is checked against them. A missing card is a `card_missing` event, not an error, unless `skill`
   needs it. When the card says it `supportsAuthenticatedExtendedCard` and the target sends
   credentials, the **extended card** is read too (`agent/getAuthenticatedExtendedCard`, `GET
   /v1/card` or `GetAgentCard` with the credentials) and replaces the public one: a `card_extended`
   event says how many skills it added, and `skill` may name one of them. A card that declares
   `security` while the target sends no credentials is an `auth_missing` event.
2. The **transport** is chosen (see below) and recorded as a `transport` event. Each user turn
   goes out as a `message` with a fresh `messageId`: a text part for the words, a data part for a
   script step's `data`, a file part for each of its `files` (bytes inline, or a URI the agent
   fetches). The first message of a session opens a task; the agent's `contextId` is kept for the
   whole session.
3. The agent's turn is read from the result. For a **task**: the status message, then the text
   of the artifacts not read before and, when both are empty, the agent's messages in the history
   after ours. Data parts are rendered as JSON so a judge can read them. For a plain **message**
   result: its text parts.
4. A task in `input-required` (or `auth-required`) is **continued**: the next user turn carries
   its `taskId`. A `completed` task is followed by a **new task in the same context**, which is
   how a conversation goes on; it does not end the session. A `failed`, `canceled` or `rejected`
   task ends the session. Every state the agent reports is a `task_<state>` event.
5. With `message/send`, a task the agent left `working` is read with `tasks/get` every 500 ms
   until it settles, within `turn_timeout`. With `push: true`, the send carries a
   `pushNotificationConfig` instead and tamesu waits for the agent to **post the task** to a local
   listener (each notification is a `push_received` event and `a2a_pushes` counts them); a
   `tasks/get` every ten poll intervals covers a lost notification. With `message/stream`, events
   are read until the final status update; `time_to_first_token_ms` is the first event with text,
   and a stream that **drops** before the final event is picked up again with `tasks/resubscribe`
   (a `resubscribed` event, `a2a_resubscribes` counts them, as many attempts as `connect.attempts`).
6. Closing the session cancels a task still waiting for input with `tasks/cancel`.

Every result or streamed event of a turn is in `result.json`, so `json_path` reaches task states
(`$.status.state`), artifacts (`$.artifacts[*].name`) and data parts
(`$.artifacts[*].parts[?(@.kind == 'data')].data`), and streamed turns are a list of events. A
protocol error fails the turn with its code and message, whatever the binding.

## Transports

The protocol has three bindings and the card says which the agent speaks: `preferredTransport`
for its `url`, `additionalInterfaces` for the others. tamesu speaks all three, and every one
carries the same messages, tasks and events, so a suite reads the same whichever is used.

| `transport` | Wire | `url` |
|---|---|---|
| `jsonrpc` (default) | JSON-RPC 2.0 over HTTP POST, server-sent events for streams | the JSON-RPC endpoint |
| `http` | the HTTP+JSON binding: `POST /v1/message:send`, `/v1/message:stream`, `GET /v1/tasks/{id}`, `/v1/tasks/{id}:cancel`, `:subscribe`, `/pushNotificationConfigs`, `GET /v1/card` | the base the `/v1` paths hang off |
| `grpc` | the `A2AService` gRPC service (`grpc://` plaintext, `grpcs://` TLS, `host:port` TLS unless loopback); `headers` travel as metadata | the gRPC address |

Without `transport`, the card's `preferredTransport` is used when the card is about this `url`,
`grpc` for a `grpc://` URL, else `jsonrpc`. With `transport` set to a binding the card lists under
another URL, tamesu goes to that URL: `url: https://agents.example.com/planner` with
`transport: grpc` reaches the gRPC interface the card names.

## Files and data from the user

A script step sends more than words: `data` is a data part (an order, a form), `files` are file
parts, each a path relative to the suite (its bytes go inline) or a URI the agent fetches itself,
with the name and MIME type from the path or given as `{ path, name, mime }`. The transcript
shows the data as JSON and each file as `[file name]`, so judges and `golden` files see them.

```yaml
scenario:
  script:
    - say: "Here is the damaged pair"
      files: [fixtures/sneakers.jpg]
    - data: { order: ORD-1001, reason: damaged }
```

The persona only speaks, so files and data come from scripts. Agents that answer with file parts
read as `[file name]` in the transcript; their JSON stays in `result.json`.

## Your agent's remote agents

An orchestrator rarely works alone: it delegates to other agents over A2A. To test the
orchestrator without those agents, declare them under `remote_agents` and tamesu runs them for
the duration of each run: a card with a name and skills, and scripted replies. Point the agent
under test at `{{remote.<name>.url}}` (its card is at `/.well-known/agent-card.json`, its
JSON-RPC endpoint at the URL or `/a2a`, the HTTP+JSON binding under `/v1`, and the card's
`additionalInterfaces` names a gRPC port too), or give the agent a fixed `addr` when it is
configured by hand. Every message the orchestrator sends is recorded as a **delegation**, with
the reply and the binding it came in on, and `delegated_to` asserts on it.

A remote agent speaks the whole protocol: `message/send` and `message/stream`, `tasks/get`,
`tasks/list`, `tasks/cancel` (a finished task is not cancelable), `tasks/resubscribe`, the four
`tasks/pushNotificationConfig` methods and push notifications (a send that asks for them gets
the task back as `submitted` and the finished task posted to the URL, with the caller's token in
`X-A2A-Notification-Token`), and `agent/getAuthenticatedExtendedCard`. `extended` gives the
extended card more skills or another description, so an orchestrator that reads it can be told
apart from one that does not; `token` makes every request need `Authorization: Bearer <token>`
(the card declares the scheme, anything else gets 401 and is not a delegation).

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
    extended: { skills: [bulk-track], description: "Tracking, single and bulk, for partners" }
    token: "{{secrets.SHIPPING_AGENT_TOKEN}}"               # the orchestrator must send it
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
| `url` | The agent's endpoint: the JSON-RPC URL, the base of the HTTP+JSON binding or `grpc://host:port`. The card is looked for at the URL's origin and under the URL, or read with `GetAgentCard` over gRPC. |
| `transport` | `jsonrpc`, `http` or `grpc`; default the card's preferred transport for this URL, `grpc` for a `grpc://` URL, else `jsonrpc`. |
| `headers` | Request headers (`Authorization: "Bearer {{secrets.X}}"`), also sent when reading the card; gRPC metadata. With credentials the authenticated extended card is read. |
| `stream` | Use `message/stream` (server-sent events) instead of `message/send`. When the card says the agent does not stream, `message/send` is used and a `no_streaming` event recorded. A dropped stream is resubscribed. |
| `push` | `true`: a working task's updates arrive as push notifications on a local listener (a free port on 127.0.0.1) instead of polling. `{ addr, url }` when the agent reaches tamesu through another address, such as a tunnel. When the card says the agent does not push, polling is used and a `no_push` event recorded. |
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
| `a2a_requests` | requests of the session (sends, polls, cancels), whatever the binding |
| `a2a_skills` | skills on the agent card (the extended one when it was read) |
| `a2a_pushes` | push notifications received (with `push`) |
| `a2a_resubscribes` | streams picked up again with `tasks/resubscribe` (with `stream`) |

Events: `card`, `card_extended`, `card_missing`, `auth_missing`, `transport`, `no_streaming`,
`no_push`, `push_received`, `resubscribed`, `task_submitted`, `task_working`,
`task_input_required`, `task_auth_required`, `task_completed`, `task_failed`, `task_canceled`,
`task_rejected`.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except the voice ones and the tool
ones (A2A has no tool calls): `judge`, `contains`, `regex`, `similar_to`, `grounded` with a
`text` or `json_path` context, `json_path` over the task, `captured`, `golden`, `metric`,
`turns`, `http` and `command`, and the packs.

## Example

[`examples/a2a-agent`](https://github.com/xavidop/tamesu/tree/main/examples/a2a-agent) is a
small shop agent (order status and returns, with streaming, an input-required turn, push
notifications, `tasks/resubscribe` and an authenticated extended card) and the suite that tests
it, including a push-notification test and a `data` step; `make integration` builds it and runs
the suite with the fake models.
