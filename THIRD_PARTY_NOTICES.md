# Third-party notices

tamesu is MIT licensed (see `LICENSE`). It ships or uses the following third-party material.

## Bundled in the binary

| Component | Where | License |
|---|---|---|
| [axe-core](https://github.com/dequelabs/axe-core) 4.14.0, the accessibility engine behind the browser `a11y` action | `targets/browser/assets/axe.min.js`, injected into the page under test | Mozilla Public License 2.0 (`targets/browser/assets/AXE-LICENSE`) |
| [Silero VAD](https://github.com/snakers4/silero-vad) v5, the neural voice activity detector of `vad: silero` | `audio/vad/silero_vad.onnx` | MIT |
| [Pipecat](https://github.com/pipecat-ai/pipecat) frame schema, the websocket protocol of the `pipecat` target | `targets/pipecat/frames/frames.proto` and the Go code generated from it | BSD 2-Clause (header in the file) |
| The red-team packs, prompts and voice presets | `internal/config/packs`, `llm/prompts`, `audio/noise` | tamesu's own, MIT |

## Linked or loaded at run time

| Component | How | License |
|---|---|---|
| [libopus](https://opus-codec.org) | linked into the full build (`audio/opus`) for LiveKit and SIP audio; absent from the lite build | BSD 3-Clause |
| [ONNX Runtime](https://onnxruntime.ai) | loaded as a shared library when `vad: silero` is configured; installed by you | MIT |
| [Chrome for Testing](https://developer.chrome.com/blog/chrome-for-testing) | downloaded into `~/.cache/tamesu` on first use when no Chromium is installed | Google Chrome terms of service |

## Go dependencies

The modules compiled into the `tamesu` binary, with their licenses, are listed in
`THIRD_PARTY_LICENSES.csv` (module, license URL, license), generated with
[go-licenses](https://github.com/google/go-licenses) by `make notices`. They are Apache-2.0,
MIT, BSD-2-Clause, BSD-3-Clause, ISC and MPL-2.0 licensed; none is copyleft beyond file scope.

## Models and services

Model providers (Google, OpenAI, Anthropic, Deepgram, ElevenLabs, Ollama), LiveKit and the
systems under test are reached through their own APIs under your own accounts and terms; tamesu
bundles none of them.
