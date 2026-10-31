---
layout: ../../layouts/DocsLayout.astro
title: Contributing
---

# Contributing

Contributions are welcome, especially new targets. The full guide is
[CONTRIBUTING.md](https://github.com/xavidop/tamesu/blob/main/CONTRIBUTING.md) in the repository;
this page is the short version.

## Build and test

You need Go 1.27 or newer.

```sh
git clone https://github.com/xavidop/tamesu
cd tamesu
make build       # every module; the binary is cmd/tamesu/tamesu
make all         # what CI runs: fmt, vet, check-deps, check-claims, schema-check, test, lint
```

The repository is a multi-module monorepo tied together by `go.work`: a light core at the root
(the public API, the `target`, `assert` and `metrics` contracts, config, secrets, redaction and
the CLI package) and one module per heavy dependency set, so that importing the core never pulls
Genkit, LiveKit, chromedp or a cloud SDK. `make check-deps` enforces it.

## Rules of the house

- Every model call goes through the `llm` module (Genkit); every secret through mamori.
- Everything that talks to the network sits behind an interface with a fake in tests;
  integration tests are tagged `integration` and need Docker.
- Errors are wrapped with `%w` and name the suite file, test and YAML line.
- [Conventional Commits](https://www.conventionalcommits.org/): releases and the changelog are
  generated from them.
- The README, the docs and the agent skill are updated with every change; `make check-claims`
  fails when they name a command, flag or target the CLI does not have.

## Adding a target

A target is a Go module under `targets/<name>` that implements `target.Target` and
`target.Session` (or `target.BrowserSession`) and passes the conformance suite. Open an issue
first; the checklist is in CONTRIBUTING.md.
