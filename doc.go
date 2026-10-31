// Package tamesu is the root of tamesu (試す, "to try / to put to the test"),
// an agentic testing framework and CLI for web apps and AI agents: "Put your
// web apps and AI agents to the test."
//
// Tests are written in YAML suites or in Go and run against four kinds of
// target with one runner, one assertion model and one report format:
// browser (agentic web tests), rest (HTTP agents), livekit (realtime voice
// over WebRTC) and sip (phone calls through a LiveKit SIP service).
//
// The repository is a multi-module monorepo. This root module holds the public
// API (Run, Test, Persona, Scenario, LoadSuite), the target, assert and
// metrics contracts, the reporters and the internal config, runner, secrets
// and redaction packages. It never depends on Genkit, LiveKit, pion, chromedp
// or a cloud SDK. The heavy dependency sets live in their own modules: llm
// (every model call, through Genkit), targets/rest, targets/livekit,
// targets/sip, targets/browser and audio. The tamesu binary (module
// cmd/tamesu) is the only module that links everything.
//
// Secrets are declared as mamori refs and resolved once per run; see
// github.com/xavidop/mamori.
package tamesu
