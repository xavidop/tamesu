# Changelog

All notable changes to tamesu are listed here, newest first. Versions follow [Semantic Versioning](https://semver.org/). Each new release is added at the top, with notes generated from the commit messages.

# [1.4.0](https://github.com/xavidop/tamesu/compare/v1.3.0...v1.4.0) (2026-11-03)


### Features

* tool argument schemas, similar_to assertions and transport retries ([fc73d68](https://github.com/xavidop/tamesu/commit/fc73d6820e94286c6ffca9cc012053ed9bc10aa0))

# [1.3.0](https://github.com/xavidop/tamesu/compare/v1.2.0...v1.3.0) (2026-11-03)


### Features

* **llm:** retry model calls and fall back to other models ([86f1a6e](https://github.com/xavidop/tamesu/commit/86f1a6ed09ec1b42063ae44a47728f395537157f))

# [1.2.0](https://github.com/xavidop/tamesu/compare/v1.1.0...v1.2.0) (2026-11-03)


### Features

* **browser:** port the browser target to chromedp 0.21 ([082d13c](https://github.com/xavidop/tamesu/commit/082d13cc68353e44f8013a4ab2b811179384cbd8))
* **model:** the model target, a system prompt and tools tested directly against a model ([fb2f34e](https://github.com/xavidop/tamesu/commit/fb2f34e59a9eccc100e14d30535dff103387d7e2))
* **twilio:** the Twilio Media Streams target ([134a7ad](https://github.com/xavidop/tamesu/commit/134a7ad492e13aa3211cac09b920407dbbeb45c1))

# [1.1.0](https://github.com/xavidop/tamesu/compare/v1.0.1...v1.1.0) (2026-11-03)


### Features

* **release:** Sigstore signatures for checksums.txt and the Docker image ([22c110c](https://github.com/xavidop/tamesu/commit/22c110c71ba8c7c846439864cea4c8dbd5a3706c))
* tamesu merge and tamesu history, with a history section in the HTML report ([847a4ff](https://github.com/xavidop/tamesu/commit/847a4ff8a4e1cdd5a12a3e54913f14f7ce665f57))

## [1.0.1](https://github.com/xavidop/tamesu/compare/v1.0.0...v1.0.1) (2026-11-02)


### Bug Fixes

* close websockets once, x/net 0.60 in the livekit example, completions hook through bash ([938743c](https://github.com/xavidop/tamesu/commit/938743c75a8d7825339e717b31fc2dc56bc65e5d))
* **deps:** go-jose 4.1.6 for the JWK and JSON parsing advisories ([c9f6a14](https://github.com/xavidop/tamesu/commit/c9f6a1491dcfeebb0eed9b2ebcc731e56c8b1944))

# 1.0.0 (2026-11-02)


### Bug Fixes

* **browser:** draw every recording frame onto one canvas ([cfd0d4d](https://github.com/xavidop/tamesu/commit/cfd0d4db30e0e7c0e855f9ca3eb1335b075c17b6))
* **cache:** write llm replay and action cache entries atomically ([8c47abd](https://github.com/xavidop/tamesu/commit/8c47abda01facf5fe84f4f95a15a75ae89beb73c))
* **ci:** libopus for govulncheck, absolute snapshot path in the release smoke test, doctor on the scaffold ([6e971de](https://github.com/xavidop/tamesu/commit/6e971def97bf0acaad985404d95974cc9570810e))
* **examples:** give the sip-ivr Asterisk its prompts ([c1a4933](https://github.com/xavidop/tamesu/commit/c1a4933419979ef4ef18c708159af184537de4f7))
* **llm:** strict output schemas for OpenAI models ([6013a9e](https://github.com/xavidop/tamesu/commit/6013a9e56139118a91b34d588ea403e44be988c0))
* **llm:** use the typed action constructors Genkit recommends ([ecc14cf](https://github.com/xavidop/tamesu/commit/ecc14cf202f79a0dcf77d76918910ea95a8934b5))
* **realtime:** simplify the cancelled-status check (lint) ([bea537b](https://github.com/xavidop/tamesu/commit/bea537b889d610a63083eb8f432782cac4248a39))
* **report:** check the error returns the linter flagged ([5941d0d](https://github.com/xavidop/tamesu/commit/5941d0d207b2c9b4dc56540b70c846d4f7215110))
* **secrets:** resolve the LiveKit credentials for sip tests ([69fe121](https://github.com/xavidop/tamesu/commit/69fe1214b5d714e38f32e4ef6e4e802e5d6bced9))


### Features

* **assert:** golden transcripts and reference answers ([f7cf644](https://github.com/xavidop/tamesu/commit/f7cf64488d66f1e7a52e5d3c6f4beb9ce0a07bc7))
* **assert:** red-team packs and not_regex ([1f5a20a](https://github.com/xavidop/tamesu/commit/1f5a20a84098bba0d1915d5bbf91376d7e4df5db))
* **assert:** tool_sequence ([f64e2b0](https://github.com/xavidop/tamesu/commit/f64e2b06f10d0a98bd28b1325d7a2d4d2d77de87))
* **audio:** audio module: PCM, WAV, energy and Silero VAD, noise, G.711, line simulation, Opus ([b2b8622](https://github.com/xavidop/tamesu/commit/b2b8622c6f9bd3bbbfd82c9682c799eb32671333))
* **browser:** network mocks, accessibility and visual checks ([d4a4fea](https://github.com/xavidop/tamesu/commit/d4a4feaa27e88777b96cecfcc16686e14f43bf37))
* **browser:** record the session as a video next to the screenshots ([206729a](https://github.com/xavidop/tamesu/commit/206729a74a9a443ab384c975062666a5ad5a91b5))
* **browser:** the action cache with --replay and --update-cache ([8d9b0ea](https://github.com/xavidop/tamesu/commit/8d9b0ea9358b2ae0fc52ee30f284a466dd6b035c))
* **browser:** the browser target: chromedp driver, the browser agent with Genkit tools, expect checks, deterministic actions, screenshots and HAR ([202c712](https://github.com/xavidop/tamesu/commit/202c71264c624517da6c7056b04639f322083693))
* **cli,docs:** tamesu sip setup, the sip doctor check and allowed-numbers guard, the Asterisk phone example ([81e18a8](https://github.com/xavidop/tamesu/commit/81e18a88fc49a4514bcfd626fa4a353783b9b8db))
* **cli:** cobra root with version, validate, list, init and doctor ([1522b29](https://github.com/xavidop/tamesu/commit/1522b29eca275110f55c579e8ba1b26eada51131))
* **cli:** tamesu mcp serves the tamesu tools to coding agents over stdio ([68bf160](https://github.com/xavidop/tamesu/commit/68bf16052586717386e749eb7abd34eceb9d1960))
* **cli:** tamesu record browser and tamesu generate ([d65e6fb](https://github.com/xavidop/tamesu/commit/d65e6fb5925c7f0d7ec2ad95cc1217085ddbe186))
* **cli:** tamesu run wired to the llm provider and the rest target, with the doctor models check ([ea6c80f](https://github.com/xavidop/tamesu/commit/ea6c80f99d50811b6ba83ce231921e402a30f389))
* **cmd:** tamesu binary with the mamori cloud secret providers ([d4737e6](https://github.com/xavidop/tamesu/commit/d4737e64d82abdc5289d48bedd101ae16cbd1846))
* **config:** env interpolation and template rendering ([23cedad](https://github.com/xavidop/tamesu/commit/23cedad42d6ba8b44d1ba42c209767b1e3252034))
* **config:** parameterised tests ([88bf746](https://github.com/xavidop/tamesu/commit/88bf746d6a342158a30797055342c69d46b6a923))
* **config:** suite and project types with generated JSON Schema ([7b92410](https://github.com/xavidop/tamesu/commit/7b92410f5d8f0d3c413c2bed68f59d40105a621f))
* **config:** suite loading with line-numbered validation ([47e3727](https://github.com/xavidop/tamesu/commit/47e3727b8eaf079b5f2a62922bdd7d234bc5517e))
* **config:** suite-wide assertions and SLOs ([4e95660](https://github.com/xavidop/tamesu/commit/4e9566018bfa22b7ea1d6fc6b2c40169c6935653))
* **examples:** Genkit rest-agent with scripted and broken modes, and the end-to-end integration script ([955aa55](https://github.com/xavidop/tamesu/commit/955aa55d3aa1813c73f51f75ecce7822491488d2))
* **grpc:** a generic gRPC turn API target ([1d3fe7f](https://github.com/xavidop/tamesu/commit/1d3fe7fbbb51ab6cb8c3b433d58aaeb688f4f8a8))
* import recorded conversations and calibrate the judge ([b1cfc85](https://github.com/xavidop/tamesu/commit/b1cfc85c69b6907f66e0431de3a51a858c1e5212))
* **livekit:** audio mode: TTS in, VAD and STT out, interruptions, noise, line simulation, stereo WAV ([4996bf2](https://github.com/xavidop/tamesu/commit/4996bf230b2f2a4e41303420bc02265f5c4213af))
* **livekit:** call hooks for the sip target: CallControl, sip.callStatus events and metrics, DTMF relay, dead calls ([61afd59](https://github.com/xavidop/tamesu/commit/61afd59184aa082f7d0a7a92e77dbf302511a2f5))
* **livekit:** LiveKit target in text mode with the example agent, doctor check and integration stage ([b71cac1](https://github.com/xavidop/tamesu/commit/b71cac18dafed5a0b86ac1dc85906b2b2216f1a0))
* **llm:** Genkit client with simulator and judge flows, evaluators, pricing and the llmfake provider ([de40f5d](https://github.com/xavidop/tamesu/commit/de40f5d22b03f52da0f3a12451736687cfc4925c))
* **llm:** simulator and judge replay ([a8f5d0a](https://github.com/xavidop/tamesu/commit/a8f5d0a44b22406272592207a74913fb1acac9b0))
* **llm:** speech through Genkit: synthesize/transcribe flows, Deepgram and ElevenLabs plugins, fake tts/stt ([70b59e2](https://github.com/xavidop/tamesu/commit/70b59e29b24f2d99ee5232975c26abf9718e3b39))
* **mcp:** bearer token for the HTTP server ([5f07bee](https://github.com/xavidop/tamesu/commit/5f07bee5eebd004bdfecdaf92458a20214fd1d68))
* **mcp:** serve over HTTP and expose run directories as resources ([d11f808](https://github.com/xavidop/tamesu/commit/d11f808d2667f42d90afd4b803ccee9b133ec59b))
* **persona:** scenario scripts: say, dtmf and wait_for steps before the free conversation ([e15ad61](https://github.com/xavidop/tamesu/commit/e15ad619ab3f778e77965b6896607a5d2c20f96c))
* **pipecat:** a target for Pipecat's websocket transport ([5e146c0](https://github.com/xavidop/tamesu/commit/5e146c0a127abbf43687ca667f77afd740f430a1))
* public root package with target, assert and metrics contracts ([954bb87](https://github.com/xavidop/tamesu/commit/954bb87300b06b7cdcedd72632a4616b0dd40fdc))
* quarantine known-flaky tests ([e6315ec](https://github.com/xavidop/tamesu/commit/e6315eccc6f61ee7ec009ba0e235e7b7193e4e17))
* **realtime:** answer function calls, interrupt in audio mode, decode G.711 ([d130235](https://github.com/xavidop/tamesu/commit/d130235f961ea19a5d7053c2c74914ca7db1b46c))
* **realtime:** the realtime target for the OpenAI Realtime protocol ([4439629](https://github.com/xavidop/tamesu/commit/4439629e332e6414ad78ed2623b79b0f661ab873))
* **redact:** secret redaction for logs and artifacts ([08e0423](https://github.com/xavidop/tamesu/commit/08e0423446f32b39b8b018409bed21a370ecbb65))
* **report:** github, markdown and slack reporters ([f522c29](https://github.com/xavidop/tamesu/commit/f522c298ad714d810081db6f3bc6ac08b7ca8fb1))
* **report:** latency histogram for load mode ([0aca149](https://github.com/xavidop/tamesu/commit/0aca1493236ed1d617184b1be93dc5e5c1c2e18c))
* **report:** regression against a baseline ([ec0011e](https://github.com/xavidop/tamesu/commit/ec0011e7afed20418a6723a59c9db569a2704ec7))
* **report:** the HTML report and tamesu report ([b717032](https://github.com/xavidop/tamesu/commit/b717032d6c48837907e592ded3a57624f88af2f2))
* **rest:** REST target with JSON and SSE turns, plus the targettest conformance suite ([e31ed95](https://github.com/xavidop/tamesu/commit/e31ed95bb468c7dd16fdac7052047855ec7c98b3))
* **rest:** websocket streaming with request.open, response.turn_done and response.idle ([47ed6a0](https://github.com/xavidop/tamesu/commit/47ed6a05a94c8aaa4d6ebf9996e80f297054782f))
* **run:** CI sharding and --fail-fast, with examples for import and calibrate ([0f28045](https://github.com/xavidop/tamesu/commit/0f2804513d0407b77a6f0c49010d0885a3f480ce))
* **run:** labels on every run ([951fc1c](https://github.com/xavidop/tamesu/commit/951fc1c85a57fc5b76974d4bd000f9186a87c6e2))
* **runner:** cap concurrent runs per target type (livekit rooms, sip calls) ([9262001](https://github.com/xavidop/tamesu/commit/9262001367da3a3a842b96b5d1e8672a885ff4af))
* **runner:** capture values across turns ([4404599](https://github.com/xavidop/tamesu/commit/44045994ef35964676220e4cd9732be178c08f93))
* **runner:** load mode with --load and --load-duration ([dcfe4e0](https://github.com/xavidop/tamesu/commit/dcfe4e02be4164f045a568e5ef0d01541df1d708))
* **runner:** persona loop, runner with repeats, retries, timeouts and budgets, built-in assertions and console/json/junit reporters ([784dd70](https://github.com/xavidop/tamesu/commit/784dd70ca213650154434b26227b8ea4a3732e23))
* **runner:** setup and teardown hooks ([ce4c984](https://github.com/xavidop/tamesu/commit/ce4c98482839dc3a9646d6b15bbcdb886a519c58))
* **secrets:** resolve declared secrets once per run through mamori ([b2bf127](https://github.com/xavidop/tamesu/commit/b2bf1275e9ced8277a19fa6ff8c83715a29d4a3f))
* **sip:** the phone target: outbound dialing and inbound triggers through LiveKit SIP, allowed-numbers guard, idempotent setup ([3824764](https://github.com/xavidop/tamesu/commit/38247647882fd0a1ec31846c682039fabf033ad7))
* **vad:** load onnxruntime from the usual places and ship Silero in the full build ([4071137](https://github.com/xavidop/tamesu/commit/40711375389b2752bd244854338204b430e21ab8))
