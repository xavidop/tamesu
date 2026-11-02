---
layout: ../../layouts/DocsLayout.astro
title: Roadmap
---

# Roadmap

tamesu is built phase by phase, each ending with acceptance criteria that must pass before the
next starts. Done: the suite schema and validator, the secrets layer, `init`, `validate`, `list`
and `doctor`; the runner with repeats, retries, timeouts and budgets; the Genkit layer with the
simulator and judge flows, evaluators, cost accounting and the fake provider for CI; the `rest`
target; `tamesu run` with console, JSON and JUnit reporters; the Go API; the `livekit` target in
text and audio mode with its example agent, the `synthesize` and `transcribe` flows, the
`deepgram` and `elevenlabs` Genkit plugins and the audio pipeline (VAD, noise, line simulation,
Opus, stereo WAV); the `sip` target with `tamesu sip setup`, outbound and inbound calls, DTMF,
`wait_for`/`say` scripts, call events and metrics, and the Asterisk-based example; the `browser`
target on chromedp with the browser agent, `expect` checks, deterministic actions, screenshots
and HAR, and the demo shop example; and, for v1.0, the action cache with `--replay` and
`--update-cache`, `tamesu record browser`, `tamesu generate` and the HTML report with
`tamesu report`. That was v1.0.

Since then (v1.1): `streaming: websocket` on the `rest` target (one connection per session,
`request.open`, `response.turn_done`, `response.idle`); the `realtime` target for agents speaking
the OpenAI Realtime protocol, in text and audio; `tamesu mcp`, the tamesu tools served to coding
agents over the Model Context Protocol on Genkit's MCP server; the red-team packs
(`prompt_injection`, `pii_leakage`, `system_prompt_leakage`, and suite-defined packs) with the
`not_regex` assertion; and load mode (`--load`, `--load-duration`) with a load profile per test in
the summary. This page is what comes next, in order.

## Planned

### Ideas, not yet scheduled

- Realtime target: answer the agent's function calls from a suite-defined table
  (`function_call_output`), interruptions and barge-in in audio mode, G.711 output formats.
- `tamesu mcp` over HTTP for remote agents, and MCP resources for the run directories.
- More transports as they are asked for (Pipecat, WhatsApp, a generic gRPC turn API); the
  [contributing guide](/docs/contributing/) has the checklist for a new target.
- Load mode in the HTML report as a latency histogram per test.
