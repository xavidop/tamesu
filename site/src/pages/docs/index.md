---
layout: ../../layouts/DocsLayout.astro
title: Introduction
---

# tamesu

`tamesu` (試す, "to try / to put to the test") is an open-source, agentic testing framework and
CLI written in Go. You describe a test the way you would brief a colleague: a **persona**, a
**goal**, and what a good outcome looks like. A simulated user then talks to your agent or drives
your web app, and deterministic checks and LLM **judges** grade the result. **Put your web apps
and AI agents to the test.**

![tamesu init, validate, list and doctor in a terminal](/demo.gif)

## Quick start

```sh
brew install xavidop/tap/tamesu        # macOS; Windows: scoop install tamesu; Linux: a .deb, .rpm or .apk
tamesu init                            # tamesu.yaml + examples/support-agent.yaml
tamesu validate                        # file:line errors for anything wrong
tamesu doctor                          # resolve every secret through mamori and check the setup
tamesu run                             # run the suites: transcripts, metrics, junit.xml, exit code
```

The [Quick start](/docs/quickstart/) walks through a first project.

## What you can do

| Task | Where to read |
|---|---|
| Write a suite: targets, personas, scenarios, steps and assertions in YAML | [Suites](/docs/suites/) |
| Describe a REST agent, a LiveKit voice agent, a phone agent or a web app as a target | [Suites: Target](/docs/suites/#target) |
| Grade a conversation with an LLM judge and deterministic checks | [Concepts: assertions](/docs/concepts/#assertions) |
| Keep API keys out of suites with mamori secret refs | [Configuration: secrets](/docs/configuration/#secrets-with-mamori) |
| Validate suites with file and line numbers, in your editor and in CI | [`tamesu validate`](/docs/cli/#tamesu-validate) |
| Check the whole setup, secrets included, without printing a value | [`tamesu doctor`](/docs/cli/#tamesu-doctor) |
| Run the suites and read the reports | [`tamesu run`](/docs/cli/#tamesu-run), [Reports](/docs/reports/) |
| Test a REST agent | [REST target](/docs/targets/rest/) |
| Test a LiveKit voice agent in text mode | [LiveKit target](/docs/targets/livekit/) |
| Pick models, override prompts, see traces, count cost | [AI (Genkit)](/docs/ai/) |
| Drive tests from `go test` | [Go API](/docs/go-api/) |
| Run tamesu in GitHub Actions, GitLab CI or Docker | [CI](/docs/ci/) |

## Four targets, one model

| Target | What it is | Transport |
|---|---|---|
| `browser` | A web app, driven by an agent from natural-language goals | Chromium over CDP (chromedp) |
| `rest` | An HTTP agent that answers one user turn per request | HTTP, SSE or WebSocket |
| `livekit` | A realtime voice agent in a LiveKit room, in text or audio | WebRTC |
| `sip` | A phone agent reached through a LiveKit SIP service and your trunk | SIP / PSTN |

All four share the suite schema, the assertion model, the persona and scenario definitions, the
metrics and the report format. All four run today; the action cache, the recorder, the generator
and the HTML report come next (see the [roadmap](/docs/roadmap/)).

## All AI through Genkit, all secrets through mamori

- Every model call (the simulated user, the judges, scenario generation, speech) is a
  [Genkit](https://genkit.dev) flow. Models are Genkit references such as
  `googleai/gemini-2.5-flash` or `anthropic/claude-sonnet-4-5`, so switching provider is a
  one-line change, and every run is traceable in the Genkit Developer UI.
- Every secret is a [mamori](https://github.com/xavidop/mamori) ref (`env:`, `aws-sm://`,
  `vault://`, `op://`, ...), resolved once per run, revealed only at the moment of use and
  replaced by `‹secret:NAME›` everywhere else. See [Security](/docs/security/).

## Next steps

- [Install](/docs/install/) tamesu.
- Follow the [Quick start](/docs/quickstart/).
- Read [Concepts](/docs/concepts/) for suites, targets, personas, assertions and metrics.
- Keep the [CLI reference](/docs/cli/) at hand.
