---
layout: ../../../layouts/DocsLayout.astro
title: MCP target
---

# MCP target

![An open toolbox with glowing tools, a small robot reaching in while a figure with a clipboard watches](/img/target-mcp.jpg)

The `mcp` target tests an **MCP server**: the tools it offers, their descriptions and argument
schemas, what they answer and how they fail. A model, driven by the simulated user, gets the
server's tools and every call it makes runs on the server for real, so the test sees what an
agent built on that server would see. There is no agent prompt to maintain beyond a short
`system`: the server is the thing under test.

```yaml
targets:
  shop:
    type: mcp
    command: ./bin/mcp-server                   # a stdio server to spawn (args, env), or
    # url: http://127.0.0.1:8090/mcp           # a streamable HTTP server (headers for auth)
    system: "You are the shop assistant. Answer from the tools only."
    model: googleai/gemini-2.5-flash            # the model that drives the tools; default models.default
    done_tool: end_session                      # a tool whose call ends the session (default end_session)
    allow_tools: [lookup_policy, lookup_order]  # offer only these (default: every tool)
    max_tool_rounds: 4
    call_timeout: 30s
tests:
  - name: return after 40 days
    target: shop
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario: { goal: "Find out if order ORD-1002, delivered 40 days ago, can be returned", max_turns: 4 }
    assert:
      - tool_called: { name: lookup_policy, args: { topic: returns } }
      - grounded: { context: { tool: lookup_policy } }
      - judge: "The agent states the 30-day window and does not promise an exception"
      - pack: graceful_degradation
```

## How a conversation runs

1. tamesu connects to the server (spawning `command`, or opening `url`), completes the MCP
   handshake and lists its tools. Each tool's input schema becomes the model's tool definition.
2. The simulated user speaks; the model answers with text or tool calls. Every call goes to the
   server with the model's arguments; the server's text content (parsed as JSON when it is JSON)
   or structured content comes back as the tool's result, and an `isError` result comes back as
   `{"error": ...}` with a `tool_error` event.
3. The model's final text is the agent's turn. `max_tool_rounds` bounds the calls per turn and
   `call_timeout` bounds each call.
4. The session ends when the model calls `done_tool`, the persona stops, or `max_turns` is
   reached. Closing the session disconnects (and stops the spawned server).

Tool calls carry their results in `result.json`, so `tool_called` with `args` or `schema` checks
how the model used a tool, `grounded: { context: { tool: ... } }` checks that the agent only said
what the server returned, and `fuzz_tools` can replace a tool's answer to see the model cope. The
`mcp_tools` metric is how many tools the server offered; `tamesu coverage` lists the tools that
were called and the ones that never were.

## Options

| Field | Meaning |
|---|---|
| `command`, `args`, `env` | A stdio server to spawn: the executable (a relative path is relative to the suite), its arguments and extra environment (templates allowed). |
| `url`, `headers` | A streamable HTTP server and its request headers (`Authorization: "Bearer {{secrets.MCP_TOKEN}}"`). One of `url` or `command`. |
| `system`, `system_file` | The prompt of the driving model; a short default asks it to use the tools and report truthfully. |
| `model`, `fallback`, `temperature`, `max_tokens` | The driving model, as for the [model target](/docs/targets/model/). |
| `allow_tools` | Offer only these of the server's tools. |
| `done_tool` | The tool whose call ends the session (default `end_session`; it still runs on the server). |
| `max_tool_rounds` | Tool rounds per turn (default 5). |
| `call_timeout` | How long one tool call may take (default 60s). |

## Example

[`examples/mcp-server`](https://github.com/xavidop/tamesu/tree/main/examples/mcp-server) is a
small shop server (policies and orders) with the suite that tests it; `make integration` builds it
and runs the suite with the fake models.
