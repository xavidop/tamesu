---
layout: ../../../layouts/DocsLayout.astro
title: gRPC target
---

# gRPC target

The `grpc` target tests an agent behind **tamesu's turn API**, a small gRPC service you implement
in front of your agent in any language:
[`proto/tamesu/turn/v1/turn.proto`](https://github.com/xavidop/tamesu/blob/main/proto/tamesu/turn/v1/turn.proto).
It suits agents that live inside a service mesh, speak gRPC already, or have no HTTP endpoint
worth exposing. Text only; tool calls and the end of the session travel with the reply.

```yaml
targets:
  agent:
    type: grpc
    url: grpc://agent.internal:9090                # grpc:// plaintext, grpcs:// TLS
    headers: { authorization: "Bearer {{secrets.AGENT_TOKEN}}" }   # sent as call metadata
    session: { id: "{{uuid}}" }                    # the session id (default {{uuid}})
    turn_timeout: 60s                              # how long to wait for the end of a turn
```

## The service

```proto
service TurnService {
  rpc Converse(stream ClientEvent) returns (stream ServerEvent);  // one session per stream
  rpc Turn(TurnRequest) returns (TurnResponse);                   // the unary form
}
```

**Converse** is a session. tamesu opens the stream, sends `SessionStart{session_id, metadata}`,
then one `UserTurn{text}` per user turn, and `SessionEnd` when the test is over. For each user
turn the agent streams back any number of `TextDelta{text}` and `ToolCall{name, arguments_json}`
events, in order, and exactly one `TurnEnd{end_session}`; `end_session: true` ends the
conversation the way an agent saying goodbye does.

**Turn** is for agents that do not stream: `TurnRequest{session_id, text, metadata}` in,
`TurnResponse{text, tool_calls, end_session}` out. Implement one or both: tamesu uses `Converse`
and falls back to `Turn` when the server answers `Unimplemented`.

`metadata` carries `test`, the test's name. Headers from the suite go on every call as gRPC
metadata, so credentials are `{{secrets.NAME}}` values there, never in the URL.

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | `UserTurn` sent → `TurnEnd` received (or the unary reply) |
| `time_to_first_token_ms` | `UserTurn` sent → the first `TextDelta` |
| `turns` | the number of agent turns |
| `grpc_messages_received` | the server events of the session |

Timestamps are taken around the connection, never from a model.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except `status`, `json_path` and the
audio and phone ones; `tool_called` sees the `ToolCall` events (`arguments_json` parsed as the
arguments).

## Example

The [`examples/rest-agent`](https://github.com/xavidop/tamesu/tree/main/examples/rest-agent)
agent also serves the turn API with `--grpc :9090`, and its suite has a test over it (target
`agent_grpc`, `AGENT_GRPC_URL`). The generated Go code of the proto is
`github.com/xavidop/tamesu/targets/grpc/turnv1`; for another language, generate from the
`.proto` with your toolchain.
