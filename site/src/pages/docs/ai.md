---
layout: ../../layouts/DocsLayout.astro
title: AI (Genkit)
---

# AI: Genkit everywhere

Every model call in tamesu goes through [Genkit](https://genkit.dev), from the `llm` module: the
simulated user, the judges, the Genkit evaluators, speech (text to speech and speech to text)
and, as they land, the browser agent and scenario generation. No other module imports a model SDK (`check_ai_deps.sh` fails CI if one
does). That buys three things: every model is a Genkit reference so switching provider is a
config change, every call is a traced flow visible in the Genkit Developer UI, and usage is
counted on every response.

## Models and plugins

```yaml
genkit:
  plugins: [googlegenai, anthropic, openai, ollama, evaluators]   # only these are initialised
models:                                     # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  simulator: openai/gpt-4.1-mini
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ANTHROPIC_API_KEY: env:ANTHROPIC_API_KEY
  OPENAI_API_KEY: env:OPENAI_API_KEY
```

| Plugin | Model references | Credential |
|---|---|---|
| `googlegenai` | `googleai/gemini-2.5-flash`, `googleai/gemini-2.5-pro`, ... | `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) |
| `anthropic` | `anthropic/claude-sonnet-4-6`, ... | `ANTHROPIC_API_KEY` |
| `openai` | `openai/gpt-4.1-mini`, ... | `OPENAI_API_KEY` |
| `ollama` | `ollama/llama3.1`, any local model | none (`OLLAMA_URL` for a remote server) |
| `deepgram` | `deepgram/nova-3`, `deepgram/nova-2` (speech to text) | `DEEPGRAM_API_KEY` |
| `elevenlabs` | `elevenlabs/eleven_flash_v2_5`, `elevenlabs/eleven_multilingual_v2`, `elevenlabs/eleven_v3` (text to speech) | `ELEVENLABS_API_KEY` |
| `evaluators` | the evaluators `genkitEval/deep_equal`, `genkitEval/regex`, `genkitEval/jsonata` | none |
| `fake` | `fake/simulator`, `fake/judge`, `fake/text`, `fake/tts`, `fake/stt` | none; only in a build with `-tags llmfake` |

Credentials are the resolved secrets with those conventional names, or `models.credentials:
{ googleai: "{{secrets.MY_KEY}}" }` for another name. The provider of every configured model is
initialised even when `plugins` forgets it. `tamesu doctor` reports each model and whether its
credential resolved.

The jobs: `models.simulator` plays the user, `models.judge` grades, `models.default` is the
fallback for both (and `browser_agent` and `scenario_generator` as those land).

Every flow asks for JSON with the schema of its prompt. OpenAI models receive that schema in
the API's strict mode, which the `llm` module prepares for them (every object closed to
extra properties, every property required, optional ones nullable); the other providers get
it as written. The verified combinations are Gemini, OpenAI and Anthropic models in every
role, including `tamesu generate`.

## Flows

| Flow | Input → output |
|---|---|
| `simulateUserTurn` | persona, scenario, history, turn budget → `{ say, dtmf?, hang_up?, stop, reason }` |
| `judgeRun` | rubric, scale, transcript, tool calls, metrics → `{ pass, score, reason, evidence[] }` |
| `synthesize` | text, voice hint, language → PCM (the caller's turn, audio mode) |
| `transcribe` | PCM, language → `{ text, confidence }` (the agent's turn, audio mode) |

Each flow is one span in the trace; the simulator and judge responses carry their trace id into
`result.json`.

## Speech

```yaml
speech:
  tts: { model: googleai/gemini-2.5-flash-preview-tts, voice: en-GB-female }
  stt: { model: deepgram/nova-3, language: en }
  vad: { provider: energy }
```

Text to speech is `genkit.Generate` with a TTS model that answers with an audio media part:
Gemini TTS natively (`googleai/gemini-2.5-flash-preview-tts`, `googleai/gemini-2.5-pro-preview-tts`)
or ElevenLabs through the bundled `elevenlabs` plugin. Speech to text is `genkit.Generate` with
the agent's audio as a media part and text out: Gemini audio input natively (`googleai/gemini-2.5-flash`)
or Deepgram through the bundled `deepgram` plugin. Switching provider is a config change.

A persona's `voice` is a hint, `<language>-<gender>` such as `en-GB-female`, `es-ES-male` or
just `female`, mapped to each provider's voices (Gemini `Kore`/`Puck`, ElevenLabs
`Rachel`/`Adam`, OpenAI `nova`/`onyx`); a provider voice name or ID passes through. The hint's
language is also the TTS language when `speech.tts.language` is not set.

The two plugins, `github.com/xavidop/tamesu/llm/plugins/deepgram` and
`github.com/xavidop/tamesu/llm/plugins/elevenlabs`, are ordinary Genkit model plugins and work
in any Genkit Go app. Speech is priced from `llm/pricing.yaml` too: TTS per character and STT
per minute of audio, counted into the same budget. `tamesu doctor` has a `speech` check: the
models and their credentials, the VAD this build can run and whether libopus is linked.

## Prompts

The prompts are dotprompt files embedded in the binary, `llm/prompts/simulator.prompt` and
`llm/prompts/judge.prompt`, each with a `version:` in its front matter that is recorded with
every result. To change one, copy it to `.tamesu/prompts/<name>.prompt` in your project and
edit it; the file with the same name replaces the built-in one. Keep the output schema, the
runner depends on it.

## The Developer UI

```sh
genkit start -- tamesu run tests/support.yaml     # or genkit.dev_ui: true in tamesu.yaml
```

Every simulator and judge call appears as a trace with its input, output and usage; `result.json`
links them by `trace_ids`. `make genkit-ui` in the repository runs the example this way.
OpenTelemetry export works through Genkit's standard telemetry plugins.

## Cost

Usage is read from every response and priced from `llm/pricing.yaml` (USD per million tokens,
by model reference, longest prefix wins, unknown models cost 0). `budgets.max_cost_usd` and
`budgets.max_tokens` (or `--max-cost-usd`) stop the run when exceeded: runs in flight finish,
runs not started are marked `budget`, the exit code is 4.

## Embedding tamesu in a Genkit app

```go
client, err := llm.New(ctx, llm.Config{...}, llm.WithGenkit(g))
```

`llm.WithGenkit` reuses the app's own Genkit instance (its plugins, models, evaluators and
tracing) instead of initialising one, so an app can test its own agent in-process and judge it
with its own evaluators.

## The MCP server

`tamesu mcp` is Genkit too: the tamesu tools (`tamesu_run`, `tamesu_validate`, `tamesu_schema`
and the rest) are Genkit tools on an instance of their own, served to coding agents over the
Model Context Protocol with Genkit's MCP plugin. See [MCP server](/docs/mcp/).

## CI without keys

A binary built with `-tags llmfake` registers the `fake` plugin: `fake/simulator` and
`fake/judge` replay `TAMESU_LLMFAKE_SCRIPT` (JSON: `simulator` outputs in a cycle, a `judge`
verdict that fails any rubric containing `FAIL`, `text` replies, `stt` transcripts in a cycle),
`fake/tts` speaks a tone and `fake/stt` replays the transcripts, so the whole pipeline, audio
mode included, runs with no network. `make integration` and the CI integration job use it; a default build never
contains the fake provider.
