---
layout: ../../../layouts/DocsLayout.astro
title: REST target
---

# REST target

The `rest` target tests an agent that answers one user turn per HTTP request: a chatbot
endpoint, a webhook-style bot, an agent framework's HTTP API. tamesu sends each user turn from a
body template and reads the agent's turn back from the response with JSONPaths, from a plain
JSON body, from a stream of server-sent events, or from the messages of a websocket.

```yaml
targets:
  api:
    type: rest
    url: https://api.example.com/v1/chat
    method: POST                                  # default
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request:                                      # how a user turn is sent
      body: { session_id: "{{session}}", message: "{{message}}" }
    response:                                     # how the agent turn is read
      text: $.reply                               # JSONPath
      tool_calls: $.tool_calls[*]                 # names, or {name, args} objects
      done: $.end_session                         # a truthy value ends the session
    streaming: none                               # none | sse | websocket
    session: { id: "{{uuid}}" }                   # rendered once per run
```

Over a websocket the session is one connection; each user turn is one message out and the
messages back until one marks the turn as done, or until nothing arrives for `response.idle`:

```yaml
targets:
  ws:
    type: rest
    url: wss://api.example.com/v1/chat/ws
    streaming: websocket
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request:
      open: { type: start, session_id: "{{session}}" }   # sent once after connecting (optional)
      body: { type: message, session_id: "{{session}}", text: "{{message}}" }
    response:
      text: $.delta                               # applied to every message, deltas concatenated
      tool_calls: $.tool_calls[*]
      turn_done: $.done                           # a truthy value ends the turn
      done: $.end_session                         # a truthy value ends the session (and the turn)
      idle: 2s                                    # without a marker, a turn ends after this silence
```

## Keys

| Key | Meaning |
|---|---|
| `url` | The endpoint. Templates apply (`{{vars.x}}`, `{{secrets.X}}`). |
| `method` | HTTP method, default `POST`. |
| `headers` | Request headers. Put credentials here as `{{secrets.NAME}}`; they are substituted inside the request only. |
| `request.body` | The body template, any JSON shape. `{{message}}` is the user turn, `{{session}}` the session id, `{{dtmf}}` digits (voice scripts). Without it tamesu sends `{"message": ..., "session_id": ...}`. Over a websocket it is the message sent per turn. |
| `request.open` | `websocket` only: a message template sent once after connecting, for protocols that need a session setup message. |
| `response.text` | JSONPath of the agent's reply. Without it the whole body is the reply. Every match is concatenated, so `$.choices[*].text` works. |
| `response.tool_calls` | JSONPath of the tool calls: strings are names; objects take `name`/`tool`/`function` and `args`/`arguments`/`input` (an object or a JSON string). |
| `response.done` | JSONPath of the end-of-session flag; a truthy value (`true`, `"done"`, a non-zero number) ends the conversation. |
| `response.turn_done` | `websocket` only: JSONPath of the end-of-turn flag, checked on every message; a truthy value completes the turn. Without it (and without `response.done` firing) the turn completes after `response.idle` without a message. |
| `response.idle` | `websocket` only: the silence that completes a turn when no message marked it (default `2s`). Set it above the agent's longest pause between chunks. |
| `streaming` | `none` reads one JSON body per turn. `sse` reads `data:` events until the stream ends or `[DONE]`; `response.text` is applied to every event and the deltas are concatenated. A `text/event-stream` content type switches to SSE on its own. `websocket` keeps one connection for the session (`ws://`, `wss://`, or `http(s)://` upgraded) and reads messages per turn as described above; messages that arrive between turns (a greeting, a keepalive) are read with the next turn, and a message that is not JSON is appended to the reply as text. |
| `session.id` | The session id template, rendered once when the session starts (default `{{uuid}}`). |

A response that is not JSON is taken as the reply text, with its HTTP status recorded, so a
`502 upstream down` page fails an assertion rather than crashing the run.

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | the request was sent → the agent turn is complete (the response body was read) |
| `time_to_first_token_ms` | the request was sent → the first SSE event or websocket message (plain JSON: the end of the body) |
| `turns` | the number of agent turns |
| `http_requests` | the number of requests of the session (`none` and `sse`) |
| `ws_messages_sent`, `ws_messages_received` | the messages of the session (`websocket`) |

Timestamps are taken around the HTTP call, never from a model.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except the voice ones; `status` checks
every turn's HTTP status (over a websocket every turn carries the handshake's `101`) and
`json_path` runs against every turn's response body (every event's payload for SSE, every
message for a websocket):

```yaml
assert:
  - status: 200
  - json_path: { path: $.session_id }                 # must exist on every turn
  - json_path: { path: $.end_session, equals: true }
    on_turn: last
  - tool_called: { name: lookup_policy, args: { topic: returns } }
```

## Example

[`examples/rest-agent`](https://github.com/xavidop/tamesu/tree/main/examples/rest-agent) is a
tiny Genkit agent for a shoe store with the suite that tests it, over HTTP (`POST /chat`) and
over a websocket (`GET /ws`, chunked replies), in two project configurations: real models, and
the fake provider CI uses (`make integration`).
