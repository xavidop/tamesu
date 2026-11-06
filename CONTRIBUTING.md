# Contributing to tamesu

Thanks for your interest. Contributions are welcome, especially new targets.

## Ground rules

- Be kind. See the [Code of Conduct](CODE_OF_CONDUCT.md).
- **Never commit real credentials**: not in code, tests, fixtures, transcripts or issue reports.
- Open an issue before a large change, so we agree on scope before you build it.
- Report security problems privately, as described in [SECURITY.md](SECURITY.md).

## Build and test

You need **Go 1.27 or newer**. A plain clone builds on its own.

```bash
git clone https://github.com/xavidop/tamesu
cd tamesu
make build       # build every module; the binary is cmd/tamesu/tamesu
make all         # what CI runs: fmt, vet, check-deps, check-claims, schema-check, test, lint
```

Other targets:

```bash
make test           # test every module
make lint           # golangci-lint across modules
make vuln           # govulncheck every module, minus the IDs in .github/govulncheck-ignore (reasons inside)
make schema         # regenerate schema/*.schema.json from the Go structs
make check-claims   # the README, docs and skill against the code: commands, flags, targets
make site-build     # build the docs site (Node 22 via nvm)
make site-linkcheck # build the site and fail on a broken internal link
make demo           # record site/public/demo.gif with vhs (needs vhs, ttyd and ffmpeg)
```

Every module is built with the workspace off (`GOWORK=off`), as the Makefile does, so each
`go.mod` stays correct on its own. Integration tests that need Docker run under
`-tags integration` with `make integration`; `make all` does not run them.

## Repository layout

This is a multi-module repository tied together by a `go.work` file:

- `/` (module `github.com/xavidop/tamesu`): the core. It holds the public API (`tamesu.go`), the
  `target`, `assert` and `metrics` contracts, the reporters, and the config, secrets (mamori),
  redaction, action cache, suite generator and CLI packages under `internal/`. It must never depend on Genkit, LiveKit, pion,
  chromedp, CGO audio or a cloud SDK: `make check-deps` fails the build if it does.
- `cmd/tamesu`: the `tamesu` binary, its own module. It is the only module that links everything,
  including the mamori cloud secret providers (`aws-sm://`, `vault://`, `gcp-sm://`, `azure-kv://`,
  `k8s-secret://`, `op://`, `doppler://`, `infisical://`, `sops://`).
- `llm`: the only place a model is called, its own module built on [Genkit](https://genkit.dev):
  the simulated user and judge flows, the dotprompt files under `llm/prompts/`, cost accounting,
  Genkit evaluators, the `synthesize`/`transcribe` speech flows, the `tamesu mcp` tools on
  Genkit's MCP server and, behind `-tags llmfake`, the fake provider CI runs with.
- `llm/plugins/deepgram`, `llm/plugins/elevenlabs`, `llm/plugins/cartesia`, `llm/plugins/soniox`,
  `llm/plugins/openaispeech`, `llm/plugins/azure`, `llm/plugins/assemblyai`, `llm/plugins/rime`,
  `llm/plugins/googlespeech`, `llm/plugins/speechmatics` and `llm/plugins/gladia`:
  Genkit model plugins for the speech providers (Deepgram, ElevenLabs, Cartesia, OpenAI, Azure and
  Google Cloud both ways; Soniox, AssemblyAI, Speechmatics and Gladia speech to text; Rime text to
  speech), each its own module on
  Genkit alone, so any Genkit Go app can use them.
- `targets/rest`: the REST target (HTTP, SSE), its own module; it passes the `targettest`
  conformance suite of the root module.
- `targets/livekit`: the LiveKit target (text and audio mode), its own module on the LiveKit Go
  SDK and pion; the SDK sits behind a small room interface with a fake for the unit tests, and the
  call hooks (`CallControl`, SIP call status, DTMF) the sip target plugs into.
- `targets/sip`: the phone target, its own module: it wraps a livekit audio session with call
  control through the LiveKit SIP API (outbound `CreateSIPParticipant`, inbound triggers, hangup),
  the allowed-numbers guard and `tamesu sip setup`; the API sits behind an interface with a fake.
- `targets/pipecat`: the target for Pipecat's websocket transport (protobuf frames, vendored in
  `frames/`, and RTVI messages), its own module on `coder/websocket`; text and audio.
- `targets/twilio`: the Twilio Media Streams target, its own module on `coder/websocket` and the
  audio module: tamesu plays Twilio's side of the stream (connected, start, μ-law media, dtmf,
  marks, clear, stop) against the agent's websocket.
- `targets/model`: the model target, its own module with no provider dependency: a system prompt
  and tool definitions run through `target.ChatModel`, which the llm module implements, with the
  tool calls answered from the suite.
- `targets/mcp`: the MCP server target, its own module on the `mcp-go` client and the model
  target: the server's tools are offered to a model and every call runs on the server.
- `targets/a2a`: the Agent2Agent target, its own module: the agent card (and the authenticated
  extended card), `message/send`, `message/stream` over server-sent events and push
  notifications, tasks followed through input-required to their final state and resubscribed when
  a stream drops, artifacts read as the agent's words. JSON-RPC is hand-written on the standard
  library; the HTTP+JSON and gRPC bindings go through a2a-go's protobuf types and converters
  (`internal/pbjson` turns them into the protocol's JSON at the edge). `a2abind` gives the
  suites' remote agents (`internal/a2aserver`, root module, JSON-RPC only) the same two bindings;
  `cmd/tamesu` injects it, so the root module stays free of gRPC.
- `targets/voice`: the generic voice target, its own module on `coder/websocket`, ojg and the
  audio module: a websocket that carries audio (PCM or G.711, binary frames or base64 in JSON),
  the protocol's control messages read by JSONPath, and a `session.open` request that creates
  the call. Hosted voice platforms are recipes on top of it.
- `targets/grpc`: the target for agents behind the turn API of `proto/tamesu/turn/v1`, its own
  module on grpc-go; `turnv1` is the generated code (`make proto` regenerates it).
- `targets/realtime`: the target for agents speaking the OpenAI Realtime protocol (the API itself
  or a server fronting your agent with its events), its own module on `coder/websocket`: text mode
  (conversation items and text deltas) and audio mode (PCM16 in, audio and transcript deltas out,
  a stereo WAV), function calls recorded and a done tool that ends the session.
- `targets/browser`: the browser target, its own module on chromedp (the typed `Action[T]` API of
  v0.17 and later): Chromium discovery and download, the accessibility-tree
  observation with stable refs, the page actions the browser agent's tools call, the
  deterministic step actions, screenshots, the session recording, the HAR and the console log.
- `audio`: the audio pipeline, its own module: PCM buffers and resampling, WAV, the energy VAD
  (and Silero behind `-tags onnx`, which the full build sets; onnxruntime is loaded at run
  time), noise presets, G.711 and line simulation in
  pure Go; `audio/opus` is a cgo binding to libopus that compiles to a stub under `-tags lite`.
- `examples/rest-agent`: a tiny Genkit REST agent with the suite that tests it; `make integration`
  runs it with the fake model provider.
- `examples/livekit-agent`: a LiveKit agent (LiveKit Go SDK plus a Genkit reply flow) that also
  speaks and listens with `--audio`, and its text and audio suites; `make integration` runs them
  against `livekit-server --dev` when available.
- `examples/mcp-server`: a tiny MCP server (policies and orders over stdio or streamable HTTP)
  with the suite that tests it through the `mcp` target; `make integration` builds and runs it.
- `examples/a2a-agent`: a tiny A2A agent (the shop's order and returns skills over JSON-RPC, with
  streaming) with the suite that tests it through the `a2a` target; `make integration` builds and
  runs it.
- `examples/webapp`: Mochi Shoes, a tiny Go web shop (login, search, cart) the browser example
  tests; `examples/browser-shop` is that suite, run headless by `make integration` with the fake
  browser agent's scripted actions.
- `examples/sip-ivr`: the phone example: `examples/compose.yaml` runs livekit-server, Redis, the
  LiveKit SIP service and Asterisk as carrier and IVR, and the suite calls the IVR, navigates its
  menu with `wait_for`/`dtmf` and checks `dtmf_received` and `call_ended_by` (Docker; CI only).
- `skills/tamesu`: the agent skill.
- `site/`: the Astro documentation site.

Every module has its own `go.mod`, a `go.work` entry and a dependabot entry.

## Adding a target

A target is a Go module under `targets/<name>` that implements `target.Target` and
`target.Session` (or `target.BrowserSession`) and passes the `targettest` conformance suite. Open
an issue first. The checklist:

- **Module.** One module under `targets/<name>` with `replace github.com/xavidop/tamesu => ../..`.
  The root module never requires its SDK (`make check-deps`).
- **No model SDK.** Every model call goes through the `llm` module. `check_ai_deps.sh` fails the
  build if a target imports a provider SDK or a Genkit plugin directly.
- **Secrets.** Credentials arrive as `{{secrets.NAME}}` templates and are revealed only at the
  point of use; nothing is logged or written to an artifact unredacted.
- **Timestamps.** Metrics come from the transport layer, never from a model.
- **Tests.** Table-driven unit tests with a fake of the network; integration tests behind
  `//go:build integration` against a container.
- **Docs.** The docs page under `site/src/pages/docs/targets/`, the row in the README's targets
  table, the sidebar and the skill.
- **CI.** Its integration job, plus its dependabot and `go.work` entries.

## Commits and pull requests

- Keep pull requests focused, and reference the issue they resolve.
- Use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`,
  `chore:`, `refactor:`, `test:`, `ci:`). Releases and the changelog are generated from them:
  `fix:` makes a patch release, `feat:` a minor one and `feat!:` or `BREAKING CHANGE:` a major one.
- Scopes are `cli`, `config`, `secrets`, `runner`, `llm`, `report`, `site` and the target name
  (`rest`, `livekit`, `sip`, `browser`).
- Fill out the pull request template.
- CI must be green before review.
