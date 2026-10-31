---
layout: ../../layouts/DocsLayout.astro
title: Roadmap
---

# Roadmap

tamesu is built phase by phase, each ending with acceptance criteria that must pass before the
next starts. The suite schema, the validator, the secrets layer, `tamesu init`, `validate`,
`list` and `doctor` are done. This page is what comes next, in order.

## Planned

### Core runner, Genkit layer and the REST target

- The runner: parallelism, repeats, retries, timeouts, budgets and cancellation; the persona loop.
- The `llm` module: Genkit initialised from `tamesu.yaml` and mamori secrets, dotprompt files, the
  `simulateUserTurn` and `judgeRun` flows, Genkit evaluators, cost accounting, a fake provider
  for CI (`llmfake`), trace IDs in results and the Developer UI.
- The `rest` target (plain JSON and SSE) and the assertions `judge`, `evaluator`, `contains`,
  `regex`, `json_path`, `status`, `turns`, `tool_called`, `metric`.
- `tamesu run` with console, JSON and JUnit reporters, and an example Genkit REST agent.

### LiveKit text mode

- Room join, token generation, agent detection, data and text-stream turns, end of turn from the
  agent's state, with an example LiveKit agent and `livekit-server --dev` in Docker Compose.

### LiveKit audio mode

- `synthesize` and `transcribe` Genkit flows (Gemini TTS and audio input natively), the
  `deepgram` and `elevenlabs` Genkit plugins, publishing and subscribing to audio tracks, VAD,
  stereo WAV recording, interruptions and background noise, and the audio metrics.

### The SIP target

- `tamesu sip setup`, outbound dialing and inbound answering with triggers, DTMF, `wait_for` and
  `say` scripts, line simulation (G.711, jitter, loss), transfers, hangup reasons, the call
  timeline, `allowed_numbers` and concurrency guards, with Asterisk as the carrier in CI.

### The browser target

- chromedp, observation (accessibility tree and screenshot), the `browserStep` flow with the
  browser actions as Genkit tools, deterministic actions, `expect` checks and artifacts.

### Action cache, replay, recorder, generator and the HTML report (v1.0)

- The action cache with `--replay` and `--update-cache`, `tamesu record` for browser sessions,
  `tamesu generate` to draft suites from a PRD or a system prompt, `tamesu report` and the full
  HTML report.

### After v1.0

- WebSocket and OpenAI Realtime targets, an MCP server mode so coding agents can run and author
  tests, red-team packs as reusable assertion sets, and a load mode for concurrent calls.
