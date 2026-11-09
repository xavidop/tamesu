---
layout: ../../layouts/DocsLayout.astro
title: AI (Genkit)
---

# AI: Genkit everywhere

![Model nodes around a hub, with a dotted path handing work from a dim node to a glowing one](/img/ai.jpg)

Every model call in tamesu goes through [Genkit](https://genkit.dev): the simulated user, the
judges, the Genkit evaluators, speech (text to speech and speech to text), the browser agent and
scenario generation. Every model is a Genkit reference, so switching provider is a config change;
every call is a traced flow you can open in the Genkit Developer UI; and usage is counted and
priced on every response.

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
| `openai` | `openai/gpt-4.1-mini`, ... and the speech models `openai/gpt-4o-mini-tts`, `openai/tts-1` (text to speech), `openai/gpt-4o-transcribe`, `openai/whisper-1` (speech to text) | `OPENAI_API_KEY` |
| `ollama` | `ollama/llama3.1`, any local model | none (`OLLAMA_URL` for a remote server) |
| `deepgram` | `deepgram/nova-3`, `deepgram/nova-2` (speech to text), `deepgram/aura-2`, `deepgram/aura-2-thalia-en` (text to speech) | `DEEPGRAM_API_KEY` |
| `elevenlabs` | `elevenlabs/eleven_flash_v2_5`, `elevenlabs/eleven_multilingual_v2`, `elevenlabs/eleven_v3` (text to speech), `elevenlabs/scribe_v1` (speech to text) | `ELEVENLABS_API_KEY` |
| `cartesia` | `cartesia/sonic-3.6`, `cartesia/sonic-3` (text to speech), `cartesia/ink-whisper` (speech to text) | `CARTESIA_API_KEY` |
| `soniox` | `soniox/stt-async-v6`, `soniox/stt-rt-v3` (speech to text; stt-rt-v3 is the real-time model) | `SONIOX_API_KEY` |
| `assemblyai` | `assemblyai/universal`, `assemblyai/slam-1`, `assemblyai/nano` (speech to text) | `ASSEMBLYAI_API_KEY` |
| `azure` | `azure/tts` (text to speech, the voice picks the neural model), `azure/stt` (speech to text, fast transcription) | `AZURE_SPEECH_KEY` and `AZURE_SPEECH_REGION` |
| `rime` | `rime/arcana`, `rime/mistv2` (text to speech) | `RIME_API_KEY` |
| `googlespeech` | `google/chirp3-hd`, `google/neural2`, `google/studio` (text to speech, the voice family), `google/chirp_3`, `google/chirp_2`, `google/long` (speech to text, Speech-to-Text v2) | `GOOGLE_CLOUD_API_KEY` (or `GOOGLE_API_KEY`); speech to text also needs `GOOGLE_CLOUD_PROJECT` and optionally `GOOGLE_CLOUD_LOCATION` |
| `speechmatics` | `speechmatics/enhanced`, `speechmatics/standard` (speech to text, batch) | `SPEECHMATICS_API_KEY` |
| `gladia` | `gladia/solaria-1` (speech to text, pre-recorded) | `GLADIA_API_KEY` |
| `evaluators` | the evaluators `genkitEval/deep_equal`, `genkitEval/regex`, `genkitEval/jsonata` | none |
| `fake` | `fake/simulator`, `fake/judge`, `fake/text`, `fake/tts`, `fake/stt` | none; only in a build with `-tags llmfake` |

Credentials are the resolved secrets with those conventional names, or `models.credentials:
{ googleai: "{{secrets.MY_KEY}}" }` for another name. The provider of every configured model is
initialised even when `plugins` forgets it. `tamesu doctor` reports each model and whether its
credential resolved.

The jobs: `models.simulator` plays the user, `models.judge` grades, `models.browser_agent` drives
the browser, `models.scenario_generator` drafts suites, and `models.default` serves every job
without a model of its own. `models.embedder` is the embedding model behind
[`similar_to`](/docs/assertions/) assertions (`googleai/text-embedding-004`,
`googleai/gemini-embedding-001`, `openai/text-embedding-3-small`, ...); when unset, tamesu uses
the usual embedder of the default model's provider, and Anthropic has none, so a suite that
judges with Claude and compares meaning needs `embedder` set. Gemini, OpenAI and Anthropic models can take any of the roles; every
flow asks the model for JSON in the shape its prompt declares, in the provider's
structured-output mode.

## Retries and fallbacks

A provider that rate-limits, times out or is briefly down should not fail a test, and a
provider that is down for the afternoon should not fail the run. Every model call tamesu makes
is retried, and can hand over to another model:

```yaml
models:
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  fallback:
    default: [openai/gpt-4.1-mini]           # for every job without a list of its own
    judge: [googleai/gemini-2.5-pro, openai/gpt-4.1]
  retry: { attempts: 3, backoff: 500ms, max_backoff: 10s }   # the defaults
speech:
  tts: { model: googleai/gemini-2.5-flash-preview-tts, fallback: [elevenlabs/eleven_flash_v2_5] }
  stt: { model: deepgram/nova-3, fallback: [googleai/gemini-2.5-flash] }
```

A call is tried `attempts` times on its model when the failure is one the model may not repeat:
a rate limit (429), an overloaded or unavailable provider (5xx), a timeout, a dropped
connection, or a reply that was not the JSON the prompt asked for. The wait starts at `backoff`
and doubles each time, with jitter, up to `max_backoff`; `attempts: 1` retries nothing. A failure
the model will repeat (a bad request, an unknown model, a refused key) is not retried: the call
moves straight to the first fallback, which gets its own attempts, and so on down the list. The
run's own limits are never worked around: a spent budget or a cancelled run stops at once.

This covers every model call of every target: the simulated user and the judges of the
conversational targets, the browser agent and its `expect` checks, the scenario generator, the
embedder of `similar_to` (its own `fallback.embedder` list, since a chat model cannot embed), and
the speech models of audio mode and the phone targets. A TTS fallback on another provider speaks
with that provider's voice of the same description. The `model` target has its own
[`fallback`](/docs/targets/model/) list, since the model under test is the suite's choice.
A test's `retries` setting is a different thing: it re-runs a whole test whose infrastructure
failed.

Every retry and every handover is a warning on stderr with the job, the model and the error,
and the run's summary line and `summary.json` count them (`3 retries, 1 fallback`) next to the
model calls, so a provider having a bad day is visible without reading logs. `tamesu doctor`
lists the fallback models with their credentials like any other model.

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

Text to speech is a Genkit model that answers with audio: Gemini TTS
(`googleai/gemini-2.5-flash-preview-tts`, `googleai/gemini-2.5-pro-preview-tts`), OpenAI
(`openai/gpt-4o-mini-tts`, `openai/tts-1`), ElevenLabs (`elevenlabs/eleven_flash_v2_5`),
Cartesia Sonic (`cartesia/sonic-3.6`), Deepgram Aura (`deepgram/aura-2`), Azure AI Speech
(`azure/tts`), Google Cloud Text-to-Speech (`google/chirp3-hd`, `google/neural2`) or Rime
(`rime/arcana`) through the bundled plugins. Speech to text is a Genkit
model that takes the agent's audio and answers with text: Gemini (`googleai/gemini-2.5-flash`),
Deepgram (`deepgram/nova-3`), OpenAI (`openai/gpt-4o-transcribe`, `openai/whisper-1`),
ElevenLabs Scribe (`elevenlabs/scribe_v1`), Cartesia Ink (`cartesia/ink-whisper`), Soniox
(`soniox/stt-async-v6`), AssemblyAI (`assemblyai/universal`), Azure (`azure/stt`), Google Cloud
Speech-to-Text (`google/chirp_3`), Speechmatics (`speechmatics/enhanced`) or Gladia
(`gladia/solaria-1`).
Switching provider is a config change, and `fallback: [...]` on either names the models that
take over when the first fails (see [retries and fallbacks](#retries-and-fallbacks)).

Deepgram, AssemblyAI (`assemblyai/universal`) and Soniox (`soniox/stt-rt-v3`) transcribe as they
listen: the voice targets feed them the agent's audio as it arrives and read the transcript when
the turn ends, so a turn waits for its last words only (the `stt_ms` metric), where another model
is sent the whole turn once it is over and answers after a round trip. The twilio target always
transcribes that way; livekit, sip and voice do with `transcript_source: stt` (under `auto` the
agent's own transcript is expected, and the audio is transcribed once at the end only when none
came). A model that does not stream is buffered behind the same handle, and a stream that drops
mid-turn falls back to one call over the audio it had received, so the setting is safe with any
provider. The plugins expose this as `OpenTranscription`, through the dependency-free
`llm/plugins/speechstream` contract, for any Go program that wants a provider's streaming
transcription without its SDK.

A persona's `voice` is a hint, `<language>-<gender>` such as `en-GB-female`, `es-ES-male` or
just `female`, mapped to each provider's voices (Gemini `Kore`/`Puck`, ElevenLabs
`Rachel`/`Adam`, OpenAI `nova`/`onyx`, Deepgram Aura `thalia`/`apollo`, Azure
`en-US-AvaMultilingualNeural`/`en-US-AndrewMultilingualNeural`, Rime `luna`/`orion`, Google Cloud
`Aoede`/`Charon`); a provider voice name or ID passes through. Cartesia voices are IDs from its voice library, so name one in
the hint or `speech.tts.voice`; without one every persona speaks with the same library voice. A
Deepgram Aura voice is a model of its own (`aura-2-thalia-en`), so `deepgram/aura-2` with a
voice hint speaks with that voice, and a full voice name in the hint picks it outright. Rime
speakers belong to a model (`luna` and `orion` to Arcana), so name a Mist speaker when the model
is `rime/mistv2`. A Google Cloud voice is the family in the model plus the hint's language and
speaker: `google/chirp3-hd` with `en-GB-female` speaks as `en-GB-Chirp3-HD-Aoede`, and a full
voice name such as `en-US-Neural2-F` in the hint is used as is. The hint's language is also the
TTS language when `speech.tts.language` is not set.

Azure needs two secrets: `AZURE_SPEECH_KEY` and the resource's region in
`AZURE_SPEECH_REGION` (`westeurope`, `eastus`, ...), both declared under `secrets:` like any
other credential. Google Cloud speech takes a Cloud API key in `GOOGLE_CLOUD_API_KEY` (or the
`GOOGLE_API_KEY` Gemini already uses, when that key's project has the speech APIs enabled);
speech to text also needs the project in `GOOGLE_CLOUD_PROJECT` and, for a regional endpoint,
`GOOGLE_CLOUD_LOCATION` (default `global`). Speechmatics and Gladia transcribe as batch jobs, so
each turn's transcript takes a few seconds longer than a streaming provider's.

The plugins under `github.com/xavidop/tamesu/llm/plugins/` (`deepgram`, `elevenlabs`,
`cartesia`, `soniox`, `openaispeech`, `azure`, `assemblyai`, `rime`, `googlespeech`,
`speechmatics` and `gladia`) are ordinary Genkit model plugins and work in any Genkit Go app; `openaispeech` adds the speech models Genkit's own
OpenAI plugin leaves out, under the same `openai/` prefix. Speech is priced too, TTS per
character and STT per minute of audio, into the same budget. `tamesu doctor` has a `speech` check: the models and their credentials, the
voice activity detector and whether this build has audio support.

The voice activity detector (`speech.vad.provider`) decides when the agent is speaking:
`energy` (the default) listens for sound above the noise floor; `silero` runs the Silero neural
model and tells speech from music, tones and background noise. `silero` needs the full tamesu
build and the [onnxruntime](https://onnxruntime.ai) shared library: `brew install onnxruntime`
on macOS, your distribution's package or a release tarball on Linux, `onnxruntime.dll` on the
PATH on Windows. tamesu finds it in the usual places; `TAMESU_ONNXRUNTIME=/path/to/libonnxruntime.so`
points at another one. `tamesu doctor` says which library it loaded.

## Prompts

The prompts are dotprompt files embedded in the binary (`simulator.prompt`, `judge.prompt`,
`browser_agent.prompt`, `expect.prompt`, `scenario_gen.prompt`), each with a `version:` in its
front matter that is recorded with every result. To change one, copy it to `.tamesu/prompts/<name>.prompt` in your project and
edit it; the file with the same name replaces the built-in one. Keep the output schema, the
runner depends on it.

Dotprompt reserves a few helper names (`history`, `json`, `role`, `section`, `media`), and a
helper wins over an input with the same name: write such an input as `{{this.history}}`, not
`{{history}}`, which renders nothing. The simulator prompts read the conversation that way.

## Replay: cached turns and verdicts

Every simulator turn and every judge verdict is cached under `cache.dir/llm`, keyed by the
request itself: for the simulated user, the persona, the scenario, the conversation so far, the
turn number and the version of its prompt, so a changed prompt is never answered from old turns;
for the judge, the rubric, the scale, the model and the conversation. A repeated
run of the same conversation therefore costs nothing and gives the same answers, and the console
ends with `llm cache: N hits, M misses`. Timing metrics are not part of a key, and an agent that
answers differently changes the conversation and so the key, so a regression is never hidden by
the cache.

- `tamesu run` uses the cache and records what it had to ask the models.
- `tamesu run --replay` takes the simulated user's turns from the cache only, so a conversation
  that was never recorded fails the run instead of calling a model; judge verdicts come from the
  cache when present and from the model otherwise.
- `tamesu run --update-cache` calls the models for everything and refreshes the cache.
- `cache.llm: false` in `tamesu.yaml` turns the cache off.

Commit the cache directory when you want a pull request pipeline that runs the conversational
suites without model keys; the browser action cache lives next to it.

## The Developer UI

```sh
genkit start -- tamesu run tests/support.yaml     # or genkit.dev_ui: true in tamesu.yaml
```

Every simulator and judge call appears as a trace with its input, output and usage; `result.json`
links them by `trace_ids`. OpenTelemetry export works through Genkit's standard telemetry plugins.

## Cost

Usage is read from every response and priced from the built-in price list (USD per million
tokens, by model reference, longest prefix wins, unknown models cost 0). `budgets.max_cost_usd` and
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

`tamesu mcp` serves the tamesu tools (`tamesu_run`, `tamesu_validate`, `tamesu_schema` and the
rest) to coding agents over the Model Context Protocol. See [MCP server](/docs/mcp/).

## Running without keys

A binary built with `-tags llmfake` registers the `fake` plugin: `fake/simulator` and
`fake/judge` replay `TAMESU_LLMFAKE_SCRIPT` (JSON: `simulator` outputs in a cycle, a `judge`
verdict that fails any rubric containing `FAIL`, `text` replies, `stt` transcripts in a cycle),
`fake/tts` speaks a tone and `fake/stt` replays the transcripts, so whole suites, audio mode
included, run with no network: a dry run of your pipeline, or tests of your own tamesu setup. A
default build does not contain the fake provider.
