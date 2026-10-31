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
text mode with its example agent. This page is what comes next, in order.

## Planned

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
