---
layout: ../../../layouts/DocsLayout.astro
title: REST target
---

# REST target

![Two servers exchanging envelopes along looping arrows](/img/target-rest.jpg)

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

## Replies that arrive somewhere else

A chat channel's bot does not answer in the response to the message you post: the reply shows up
in the channel's history a moment later, or the bot calls the platform's send API. Two keys cover
those shapes, and `session.open` creates the conversation first when the platform needs one.

`response.poll` polls a second endpoint after each turn's request until a reply has content,
then (with `idle`) until nothing new arrives. The captures of `session.open` render as
`{{session.NAME}}`, the captures of the turn's own request (`request.capture`) as `{{sent.NAME}}`,
and `poll.cursor` keeps a watermark between polls as `{{cursor}}`:

```yaml
targets:
  teams:                                           # a Bot Framework bot through Direct Line
    type: rest
    url: https://directline.botframework.com/v3/directline/conversations/{{session.id}}/activities
    headers: { Authorization: "Bearer {{session.token}}" }
    session:
      open:                                        # once per session: start the conversation
        url: https://directline.botframework.com/v3/directline/conversations
        headers: { Authorization: "Bearer {{secrets.DIRECT_LINE_SECRET}}" }
        capture: { id: $.conversationId, token: $.token }
    request:
      body: { type: message, from: { id: "{{session}}" }, text: "{{message}}" }
    response:
      text: "$.activities[?(@.from.id != '{{session}}')].text"
      poll:
        url: https://directline.botframework.com/v3/directline/conversations/{{session.id}}/activities?watermark={{cursor}}
        headers: { Authorization: "Bearer {{session.token}}" }
        cursor: $.watermark                        # incremental pages: the replies accumulate
        interval: 500ms
        idle: 2s                                   # the bot may answer in several activities
```

`response.listen` serves a local URL and reads the reply from the requests the agent makes to
it. Point the bot's API base URL at `{{listen.url}}` (every Bot API or Graph API client takes
one), send it the platform's webhook payload as the turn, and take the reply out of its call:

```yaml
targets:
  telegram:                                        # a Telegram bot, with tamesu as the Bot API
    type: rest
    url: "{{vars.bot_webhook}}"                    # where the bot receives updates
    session: { id: "{{faker.number}}" }           # Telegram ids are integers
    request:
      body:
        update_id: 1
        message: { message_id: 1, chat: { id: "{{session|number}}", type: private }, from: { id: "{{session|number}}", first_name: Marta }, text: "{{message}}" }
    response:
      text: $.text                                 # the text the bot passed to sendMessage
      listen:
        addr: 127.0.0.1:8089                       # the bot's TELEGRAM_API_URL is http://127.0.0.1:8089
        path: "POST /bot*/sendMessage"             # the requests that carry replies; others are acknowledged
        session: $.chat_id                         # routes each reply to its session under --parallel
        reply: { ok: true, result: { message_id: 1, chat: { id: "{{session}}" } } }
        idle: 1s
```

### A socket.io server and a handshake

Many bots and agent runtimes speak **socket.io** rather than plain websocket messages: an
Engine.IO handshake, events named and encoded as `42["event", payload]`, pings to answer.
`protocol: socketio` makes the websocket mode do that framing: every message is a mapping with
`event` and `data` in both directions, and `request.handshake` plays the exchanges a session
needs before the first turn, each step sending a message and, with `wait`, reading until a
message satisfies a JSONPath, capturing values from it as `{{session.NAME}}`:

```yaml
targets:
  runtime:                                            # a socket.io conversational runtime
    type: rest
    url: https://runtime.example.com/v4/interact/socket   # the socket.io endpoint
    streaming: websocket
    protocol: socketio
    request:
      handshake:
        - send: { event: client.start, data: { userID: "{{session}}", projectID: "{{vars.project_id}}", authorization: "{{secrets.PROJECT_KEY}}" } }
          wait: "$[?(@.event=='client.started')].event"
        - send: { event: session.create, data: {} }
          wait: "$[?(@.event=='session.created')].event"
          capture: { session_key: "$[?(@.event=='session.created')].data.sessionKey" }
        - send: { event: action.send, data: { action: { type: launch } } }
          wait: "$[?(@.event=='action.status' && @.data.status=='completed')].event"
      body: { event: action.send, data: { action: { type: text, payload: "{{message}}" } } }
    response:
      text: "$[?(@.event=='action.trace' && @.data.trace.type=='text')].data.trace.payload.message"
      turn_done: "$[?(@.event=='action.status' && @.data.status=='completed')].event"
      done: "$[?(@.event=='session.ended')].event"
```

A JSONPath that starts with a filter (`$[?(...)]`) runs against an object payload wrapped in a
one-element list, so the filter selects the message itself and the rest of the path reads from
it; on a list payload it runs as is. This applies to `response.text`, `tool_calls`, `done`,
`turn_done`, `wait` and `capture`, in every mode. Messages that arrive during the handshake and
belong to no step (the agent's greeting) are read with the first turn.

Both modes record every poll reply or incoming request of the turn in `result.json`, so
`json_path` sees buttons, cards and attachments, and a request the platform refused (a 4xx or
5xx on the turn's own request) is the turn as it came, so `status` fails it cleanly.
[Recipes](/docs/recipes/) has Slack, Teams, Telegram and WhatsApp suites built on these keys.

## Keys

| Key | Meaning |
|---|---|
| `url` | The endpoint. Templates apply (`{{vars.x}}`, `{{secrets.X}}`). |
| `method` | HTTP method, default `POST`. |
| `headers` | Request headers. Put credentials here as `{{secrets.NAME}}`; they are substituted inside the request only. |
| `request.body` | The body template, any JSON shape. `{{message}}` is the user turn, `{{session}}` the session id, `{{dtmf}}` digits (voice scripts). A value that is exactly `"{{session\|number}}"` (any placeholder, the `number` filter) is sent as a JSON number, for APIs whose ids are integers. Without it tamesu sends `{"message": ..., "session_id": ...}`. Over a websocket it is the message sent per turn. |
| `request.open` | `websocket` only: a message template sent once after connecting, for protocols that need a session setup message. |
| `response.text` | JSONPath of the agent's reply. Without it the whole body is the reply. Every match is concatenated, so `$.choices[*].text` works. |
| `response.tool_calls` | JSONPath of the tool calls: strings are names; objects take `name`/`tool`/`function` and `args`/`arguments`/`input` (an object or a JSON string). |
| `response.done` | JSONPath of the end-of-session flag; a truthy value (`true`, `"done"`, a non-zero number) ends the conversation. |
| `response.turn_done` | `websocket` only: JSONPath of the end-of-turn flag, checked on every message; a truthy value completes the turn. A message that ends the session (`response.done`) arriving within 50 ms of the turn's end is read with the turn. Without it (and without `response.done` firing) the turn completes after `response.idle` without a message. |
| `response.idle` | `websocket` only: the silence that completes a turn when no message marked it (default `2s`). Set it above the agent's longest pause between chunks. |
| `streaming` | `none` reads one JSON body per turn. `sse` reads `data:` events until the stream ends or `[DONE]`; `response.text` is applied to every event and the deltas are concatenated. A `text/event-stream` content type switches to SSE on its own. `websocket` keeps one connection for the session (`ws://`, `wss://`, or `http(s)://` upgraded) and reads messages per turn as described above; messages that arrive between turns (a greeting, a keepalive) are read with the next turn, and a message that is not JSON is appended to the reply as text. |
| `session.id` | The session id template, rendered once when the session starts (default `{{uuid}}`). |
| `session.open` | A request made once per run, before the target starts: `url`, `method` (default `POST`), `headers`, `body` and `capture` (JSONPaths by name into its JSON reply, rendered as `{{session.NAME}}` in every later template). A 4xx or 5xx fails the run. It applies to every target type, not only `rest`. |
| `protocol` | `socketio` with `streaming: websocket`: socket.io framing; messages are `{ event, data }` both ways and the server's pings are answered. |
| `request.handshake` | `websocket` only: steps played once after connecting (after `request.open`), each with `send` (a message template), `wait` (a JSONPath that must be truthy on a received message), `capture` (JSONPaths by name into that message, as `{{session.NAME}}`) and `timeout`. |
| `response.join` | Plain JSON: the separator between several matches of `response.text` (a newline between the messages of one reply); default none. |
| `request.capture` | With `response.poll`: JSONPaths by name into the reply to the turn's request, rendered as `{{sent.NAME}}` in the poll of that turn (the timestamp of the message you posted, to poll after it). |
| `response.poll` | The reply is read by polling: `url` (templates, `{{cursor}}`), `method` (default `GET`), `headers`, `body`, `interval` (default `500ms`), `timeout` (default the target's `60s`), `idle` (keep polling after the first reply until nothing new for this long) and `cursor` (a JSONPath into each poll reply whose value is the next `{{cursor}}`; with it the polls accumulate, without it each poll replaces the last). `response.text`, `tool_calls` and `done` read the poll replies. |
| `response.listen` | The reply is read from the agent's own requests to a URL tamesu serves: `addr` (default a free port on 127.0.0.1; `{{listen.url}}` renders it), `path` (an optional method and a path with `*` wildcards; a trailing `/` matches a prefix; default every request), `session` (a JSONPath into the incoming payload holding the session id, to route under `--parallel`), `reply` and `status` answered to the agent (default `{"ok": true}`, `200`), `timeout` and `idle`. JSON, form and multipart bodies and query strings are all read; requests outside `path`, and requests without content, are acknowledged but do not complete the turn. |

A response that is not JSON is taken as the reply text, with its HTTP status recorded, so a
`502 upstream down` page fails an assertion rather than crashing the run.

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | the request was sent → the agent turn is complete (the response body was read) |
| `time_to_first_token_ms` | the request was sent → the first SSE event or websocket message (plain JSON: the end of the body; poll or listen: the first reply with content) |
| `turns` | the number of agent turns |
| `http_requests` | the number of requests of the session (`none` and `sse`) |
| `ws_messages_sent`, `ws_messages_received` | the messages of the session (`websocket`) |
| `poll_requests` | the polls of the session (`response.poll`) |
| `webhooks_received` | the requests the agent made to the listener that carried a reply (`response.listen`) |

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
over a websocket (`GET /ws`, chunked replies), in two project configurations: real models
(`tamesu.yaml`) and the fake provider (`tamesu.llmfake.yaml`, no keys needed).
