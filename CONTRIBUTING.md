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
- `llm/plugins/deepgram` and `llm/plugins/elevenlabs`: Genkit model plugins for Deepgram speech to
  text and ElevenLabs text to speech, each its own module on Genkit alone, so any Genkit Go app can
  use them.
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
- `targets/grpc`: the target for agents behind the turn API of `proto/tamesu/turn/v1`, its own
  module on grpc-go; `turnv1` is the generated code (`make proto` regenerates it).
- `targets/realtime`: the target for agents speaking the OpenAI Realtime protocol (the API itself
  or a server fronting your agent with its events), its own module on `coder/websocket`: text mode
  (conversation items and text deltas) and audio mode (PCM16 in, audio and transcript deltas out,
  a stereo WAV), function calls recorded and a done tool that ends the session.
- `targets/browser`: the browser target, its own module on chromedp (pinned to the v0.16 line,
  the last with the classic action API): Chromium discovery and download, the accessibility-tree
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
