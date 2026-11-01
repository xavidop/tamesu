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
`tamesu report`. That is v1.0. This page is what comes next, in order.

## Planned

### After v1.0

- WebSocket and OpenAI Realtime targets, an MCP server mode so coding agents can run and author
  tests, red-team packs as reusable assertion sets, and a load mode for concurrent calls.
