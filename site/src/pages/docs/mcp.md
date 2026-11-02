---
layout: ../../layouts/DocsLayout.astro
title: MCP server
---

# MCP server: tamesu for coding agents

`tamesu mcp` serves tamesu to a coding agent over the
[Model Context Protocol](https://modelcontextprotocol.io) on standard input and output, so
Claude Code, Cursor, Codex or any MCP client can write a suite, validate it, run it and read the
results without leaving the editor. Every tool runs the matching tamesu command in the same
process, with the `--config`,
`--var` and `--env-file` the server was started with, and returns the command's output and exit
code.

## Tools

| Tool | What it does | Returns |
|---|---|---|
| `tamesu_schema` | The JSON Schema of a suite file (`which: suite`, the default) or of `tamesu.yaml` (`which: config`): the reference an agent needs to write one | the schema |
| `tamesu_validate` | `tamesu validate` on `paths` (or the project's suites), `strict` to make warnings errors | `exit_code`, `stdout`, `stderr` |
| `tamesu_list` | `tamesu list --json` with the run selection (`filter`, `tags`, `target`) | the tests as JSON in `stdout` |
| `tamesu_run` | `tamesu run` with the selection, `repeat`, `parallel`, `mode`, `max_cost_usd`, `replay` and an `out` directory (a temporary one by default) | `exit_code`, `out_dir`, `summary` (the run's `summary.json`), `console` |
| `tamesu_generate` | `tamesu generate` from `source` (the text) or `file`, for `target` of `type`, `count` tests; `out` writes the suite | `exit_code`, `suite_yaml` |
| `tamesu_doctor` | `tamesu doctor` | `exit_code`, `stdout`, `stderr` |
| `tamesu_init` | `tamesu init [dir]` (`force` overwrites) | `exit_code`, `stdout` |

Exit codes mean what they mean on the command line: `0` passed, `1` a test failed, `2` a
configuration or usage error, `3` an infrastructure error, `4` the cost budget was exceeded. A
failing command is a result the agent reads, not a tool error.

Secrets are resolved per command through mamori, revealed only at their point of use and
redacted from every message, exactly as in the CLI; a tool never returns a secret. Model spend
is bounded by the project's `budgets` and the tool's `max_cost_usd`.

## Setup

The server runs in the project directory (`--cwd` picks another), so start it where
`tamesu.yaml` is, or pass `--config`:

```sh
# Claude Code
claude mcp add tamesu -- tamesu mcp --cwd /path/to/project

# Codex (~/.codex/config.toml)
[mcp_servers.tamesu]
command = "tamesu"
args = ["mcp", "--cwd", "/path/to/project"]
```

```json
// Cursor (.cursor/mcp.json) and most other clients
{ "mcpServers": { "tamesu": { "command": "tamesu", "args": ["mcp", "--cwd", "/path/to/project"] } } }
```

The server stops when the client closes the connection. With `-v` it logs to standard error,
never to standard output, which carries the protocol.

## A session

An agent asked to "add a test for the refund policy" typically calls `tamesu_schema`, reads the
suite, writes the test, calls `tamesu_validate` until the file is clean, runs it with
`tamesu_run` (`filter: refund`) and reads the assertions and their reasons from `summary`; a
failing judge comes back with its reason and evidence, so the agent can tell a bad test from a
bad agent. For a brand-new project it starts with `tamesu_init` and `tamesu_generate` from the
PRD. The [agent skill](https://github.com/xavidop/tamesu/tree/main/skills/tamesu) teaches the
same model of suites and assertions to agents that do not use MCP.
