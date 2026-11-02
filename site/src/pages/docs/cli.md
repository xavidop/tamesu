---
layout: ../../layouts/DocsLayout.astro
title: CLI reference
---

# CLI reference

Every tamesu feature is a `tamesu` subcommand. This page lists the commands and the rules they
share: global flags, exit codes and how errors are printed.

## Quick start

```sh
tamesu init                     # scaffold tamesu.yaml and examples/
tamesu validate                 # check tamesu.yaml and the suites
tamesu list --json              # list the tests
tamesu doctor                   # check the setup, secrets included
tamesu --help                   # every command
```

## Commands

| Command | What it does |
|---|---|
| [`tamesu init`](#tamesu-init) | Scaffold `tamesu.yaml` and an `examples/` suite |
| [`tamesu validate`](#tamesu-validate) | Check `tamesu.yaml` and the suites without running them |
| [`tamesu list`](#tamesu-list) | List the tests of the suites |
| [`tamesu run`](#tamesu-run) | Run the suites and write the reports |
| [`tamesu doctor`](#tamesu-doctor) | Check the config, suites, secrets, models, speech, Chromium, LiveKit and SIP |
| [`tamesu report`](#tamesu-report) | Rebuild `report.html` from a run directory |
| [`tamesu record browser`](#tamesu-record-browser) | Record a browser session as deterministic steps |
| [`tamesu generate`](#tamesu-generate) | Draft tests from a PRD, a system prompt or a knowledge base |
| [`tamesu sip setup`](#tamesu-sip-setup) | Create or reuse the LiveKit SIP trunks and the inbound dispatch rule |
| [`tamesu mcp`](#tamesu-mcp) | Serve tamesu to coding agents as an MCP server over stdio |
| [`tamesu version`](#tamesu-version) | Print the version, commit and build date |

`tamesu completion` prints a shell completion script, and `tamesu help` shows help for any
command.

## Global flags

| Flag | Meaning |
|---|---|
| `--config <path>` | Project configuration file (default `tamesu.yaml` in the working directory). See [Configuration](/docs/configuration/#where-the-file-lives). |
| `-v, --verbose` | Debug logging to stderr. Logs are redacted like everything else. |
| `--var KEY=VAL` | A ref var for `${VAR}` in secret refs and `{{vars.KEY}}` in suites. Repeatable. Wins over the suite's `vars:`. |
| `--env-file <path>` | A dotenv file read through mamori's dotenv provider when an `env:` secret or a `${VAR}` is not set in the process environment. The environment is never mutated. |

## Suite paths

`validate`, `list`, `run` and `doctor` take suite paths and globs as arguments (`**` matches across
directories; a directory means every `.yaml` and `.yml` under it). Without arguments they use the
`suites:` globs of `tamesu.yaml`, relative to that file. A pattern that matches nothing is an
error (exit `2`).

## tamesu init

```text
tamesu init [dir] [--force]
```

Writes `tamesu.yaml` (project defaults with comments) and `examples/support-agent.yaml` (a suite
with a REST and a LiveKit target) into `dir`, by default the working directory, and prints each
file it created. It refuses to overwrite an existing file and writes nothing in that case.

| Flag | Meaning |
|---|---|
| `--force` | Overwrite existing files. |

## tamesu validate

```text
tamesu validate [paths...] [--strict]
```

Loads `tamesu.yaml` and every suite and prints each problem as `file:line: path: message`, errors
and warnings alike, then a summary line such as `2 suites, 7 tests, 0 errors, 1 warning`. An
unset `${VAR}` is a warning, so a suite can be validated on a machine without the variables; a
`${VAR}` in a field documented as secret (`api_key`, `api_secret`, `headers.Authorization`,
`password`, `token`) is a warning too. Exit `2` when there is an error.

| Flag | Meaning |
|---|---|
| `--strict` | Treat warnings as errors. |

```text
examples/support-agent.yaml:40: tests[0] "refund policy question".target: target "nope" is not declared in targets (have api, voice)
examples/support-agent.yaml:12: warning: targets.voice.url: env var LIVEKIT_URL is not set; ${LIVEKIT_URL} is left as is
1 suite, 2 tests, 1 error, 1 warning
```

## tamesu list

```text
tamesu list [paths...] [--json] [--filter <regex>] [--tags a,b] [--target <name>]
```

Prints one line per test: suite, test, target, target type and tags. A suite with errors is
reported on stderr and makes the command exit `2`; warnings are not printed.

| Flag | Meaning |
|---|---|
| `--json` | Print a JSON array of `{suite, file, name, target, type, tags}` instead of a table. |
| `--filter <regex>` | Only tests whose name matches. |
| `--tags a,b` | Only tests having any of these tags (the suite's tags count for every test). |
| `--target <name>` | Only tests of this target. |

## tamesu run

```text
tamesu run [paths...] [--filter <regex>] [--tags a,b] [--target <name>] [--parallel N] [--repeat N] [--load N [--load-duration D]] [--mode text|audio] [--headed] [--replay | --update-cache] [--update-golden] [--baseline | --baseline-dir <dir>] [--out <dir>] [--reporter console,json,junit,html] [--max-cost-usd X]
```

Runs the selected tests. `tamesu.yaml` and the suites are loaded with `${VAR}` required; the
secrets the selected tests use (and the model credentials) are resolved through mamori once,
before anything runs; the AI layer is initialised from `genkit.plugins` and `models.*`; then every
test runs against its target, `repeat` times, in parallel. Each run gets a directory with
`transcript.json` and `result.json` (assertions with reasons, metrics, trace ids, prompt
versions); the reporters write `summary.json`, `junit.xml` and the console output. See
[Reports](/docs/reports/).

| Flag | Meaning |
|---|---|
| `--filter <regex>` | Only tests whose name matches. |
| `--tags a,b` | Only tests having any of these tags. |
| `--target <name>` | Only tests of this target. |
| `--parallel N` | Concurrent runs (default: the number of CPUs). |
| `--repeat N` | Override every test's repeat count. |
| `--load N` | Load mode: every selected test runs with N concurrent virtual users; `--parallel` defaults to N per test and the target caps (`livekit.max_concurrent_rooms`, `sip.max_concurrent_calls`) still apply. The summary gets a `load` profile per test and the console prints it. Excludes `--repeat`. |
| `--load-duration D` | Load mode: each virtual user keeps looping runs for `D` (such as `2m`); without it every user runs once. |
| `--mode text\|audio` | Voice tests: override the mode (livekit, sip and realtime). |
| `--headed` | Browser tests: show the browser window instead of running headless. |
| `--replay` | Replay from the caches: browser goals from their cached action plans and the simulated user's turns from cached answers, a miss failing the step or the run; judge verdicts come from the cache when present and from the model otherwise. See [Replay](/docs/ai/#replay-cached-turns-and-verdicts). |
| `--update-cache` | Ignore the caches (action plans, simulator turns, judge verdicts), call the models and refresh them. |
| `--update-golden` | Rewrite every `golden` file from this run's transcripts; see [Reference answers](/docs/judges/#reference-answers). |
| `--baseline`, `--baseline-dir <dir>` | After the run, compare it with the baseline (`baseline.dir`, default `.tamesu/baseline`, or the directory given): new failures, fixed tests, p95 latency and cost growth beyond the limits. Writes `comparison.json`, prints the verdict and exits 1 on a regression. See [Comparing with a baseline](/docs/reports/#comparing-with-a-baseline). |
| `--out <dir>` | Run directory (default `.tamesu/runs/<timestamp>`). |
| `--reporter console,json,junit,html` | Reporters to run (default `reporters:` of `tamesu.yaml`; `json` is always on). |
| `--max-cost-usd X` | Stop the run once tamesu's own model spend exceeds X (default `budgets.max_cost_usd`). |

Exit `0` when every test passed, `1` when a test failed (including a timeout), `2` on a config
error (an invalid suite, no tests selected, a secret ref with no value, an unknown reporter), `3`
when a target, a model or a secret provider was unreachable, `4` when the budget stopped the run.

A test fails when any assertion fails or it exceeds its `timeout`; an infrastructure error
(target unreachable, model error) is retried `retries` times and then counts as an error, not a
failure. With `repeat` above 1, the test passes when the fraction of passing runs reaches
`pass_threshold`; in load mode the same threshold applies to every run the virtual users made.
See [CI: load mode](/docs/ci/#load-mode).

## tamesu doctor

```text
tamesu doctor [paths...]
```

Checks the setup and prints one line per check, `ok`, `warn`, `fail` or `skip`:

- **config**: `tamesu.yaml` parses, or the defaults are in use; every error and warning of the
  file.
- **suites**: every suite loads with `${VAR}` required (an unset variable is a failure here);
  the number of files and tests.
- **secret NAME**: every declared secret, project and suites, resolved through mamori one by one.
  `ok secret API_TOKEN: env (link 1 of 2)` says which link of the chain answered; a value is never
  printed. A chain with no value is `fail` (exit `2`); a provider that failed is `fail` with the
  kind (`unavailable`, `permission_denied`, ...) and exit `3`. A value shorter than three bytes
  is `warn`: it cannot be redacted.
- **models, speech, chromium, livekit, sip**: `skip` when the configuration does not use them or
  this build lacks them. `speech` reports the TTS and STT models,
  their credentials, the VAD this build can run and whether libopus is linked; `sip` checks that
  `sip.livekit_url`, the trunk id, `inbound_number` and the LiveKit keys are there for the sip
  targets the suites declare.

Exit `0` when nothing failed, `2` when a configuration check failed, `3` when a provider or
service was unreachable. `--var` and `--env-file` apply.

## tamesu report

```text
tamesu report <run-dir>
```

Reads `summary.json` from a run directory and writes `report.html` next to it: one
self-contained page with every test's assertions and reasons, transcript, metrics, event
timeline, step screenshots and an audio player with turn markers, linking the run's artifacts
relative to itself. The `html` reporter writes the same file during `tamesu run`
(`reporters: [console, junit, html]`, or `--reporter html`). Exit `2` when the directory has no
`summary.json`. See [Reports](/docs/reports/).

## tamesu baseline

```text
tamesu baseline save <run-dir> [--dir <baseline-dir>]
```

Keeps the run that later runs are measured against. `tamesu baseline` alone lists its
subcommands.

## tamesu baseline save

Copies a run directory's `summary.json` into the baseline directory: `--dir`, else
`baseline.dir` in `tamesu.yaml`, else `.tamesu/baseline`. Run it on the branch you trust, after
a green run; `tamesu run --baseline` and `tamesu compare` read it back.

## tamesu compare

```text
tamesu compare <baseline-dir> <run-dir> [--max-latency-increase-pct N] [--max-cost-increase-pct N] [--fail-on-new-failures]
```

Reads both summaries, matches the tests by suite and name and reports new failures, fixed tests
and the tests whose `turn_latency_ms_p95`, `time_to_first_token_ms_p95`,
`time_to_first_audio_ms_p95` (medians over repeats) or `llm_cost_usd` grew beyond the limits:
`--max-latency-increase-pct` (default 20), `--max-cost-increase-pct` (default 50), 0 disables a
check; `--fail-on-new-failures` (default true) makes a newly failing test a regression. Defaults
come from `baseline:` in `tamesu.yaml`. It writes `comparison.json` into the run directory and
exits `1` when the run regressed, `0` otherwise, `2` when a summary cannot be read.

## tamesu record

```text
tamesu record <command>
```

The recorders. `tamesu record browser` turns a session you drive by hand into suite steps.

## tamesu record browser

```text
tamesu record browser <url> [--out steps.yaml]
```

Opens Chromium with a window on the URL and records what you do: clicks, typing (reported when
you leave the field or press Enter), selections, Enter, and the pages you navigate to by hand.
Each becomes a deterministic `action:` step with a durable selector (an id, a `name`, an
`aria-label`, else a short path such as `#login > button`); a password field is recorded as
`{{secrets.PASSWORD}}`, never its value. Close the browser or press Ctrl-C to finish: the
`steps:` block is printed (or written with `--out`), ready to paste into a browser test and mix
with `goal` and `expect` steps. Exit `3` when Chromium cannot start.

## tamesu generate

```text
tamesu generate --from <file> --target <name> [--type rest|livekit|sip|browser] [--count N] [--out suite.yaml]
```

Reads a PRD, a system prompt or a knowledge base and asks `models.scenario_generator` (or
`models.default`) for `--count` draft tests (default 5) for one target: personas, goals, judge
rubrics and fixed phrases for a conversational target, goals and expectations for a browser
target. It writes a suite with a target stub (`url: ${AGENT_URL}` and the usual request and
response mapping for `rest`) to review, then validates it the way `tamesu validate` would. Exit
`0` when the draft validates, `1` when it does not (the file is kept for review), `2` on a
usage or config error, `3` when the model failed. See [Generating tests](/docs/generate/).

## tamesu sip

```text
tamesu sip <command>
```

The phone target's helpers; `tamesu sip` alone lists them.

## tamesu sip setup

```text
tamesu sip setup [--outbound] [--inbound]
```

Creates, or reuses by name, what the phone target needs on the LiveKit SIP service: the
outbound trunk `tamesu-outbound` from `sip.trunk` (address, numbers, auth), the inbound trunk
`tamesu-inbound` for the trunk's numbers and the dispatch rule `tamesu-inbound` that routes
inbound calls into `sip.inbound_room`. It resolves `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` and
the trunk's auth secrets through mamori, prints what it created or reused, and ends with the
`sip:` block (`outbound_trunk_id`, `inbound_trunk_id`, `dispatch_rule_id`) to paste into
`tamesu.yaml`. Running it again changes nothing. `--outbound` or `--inbound` limit it to one
side. Exit `2` when `sip.livekit_url` or `sip.trunk` is missing, `3` when the LiveKit API
fails. See [SIP setup](/docs/sip-setup/).

## tamesu mcp

```text
tamesu mcp [--cwd dir] [--http addr] [--http-path /mcp]
```

Serves the tamesu tools over the Model Context Protocol on standard input and output, for a
coding agent (Claude Code, Cursor, Codex, ...): `tamesu_schema`, `tamesu_validate`,
`tamesu_list`, `tamesu_run`, `tamesu_generate`, `tamesu_doctor` and `tamesu_init`. Each tool
runs the matching command in the same process with the server's `--config`, `--var` and
`--env-file`, and returns its output and exit code (a run returns its `summary.json`). The run
directories under `.tamesu/runs` are resources: `tamesu://runs` lists them and
`tamesu://runs/<run>/summary.json` reads one. `--cwd` changes to the project directory first.
`--http addr` (such as `:8765`) serves over streamable HTTP at `--http-path` (default `/mcp`)
instead of stdio, for agents on another machine. Over stdio the server stops when the client
disconnects; over HTTP it runs until interrupted. `-v` logs to standard error. See
[MCP server](/docs/mcp/).

## tamesu version

```text
tamesu version
```

Prints the version, commit and build date on one line, such as
`tamesu 1.0.0 (abc1234, 2026-10-31)`.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | All tests passed (or, for `validate`, `list` and `doctor`, nothing failed). |
| `1` | One or more tests failed, or timed out. |
| `2` | Config or schema error: an invalid suite or `tamesu.yaml`, a bad flag, an unknown command, a secret ref with no value, no suites matching a path. |
| `3` | Infrastructure error: a secret provider, LiveKit, a browser or a model that could not be reached. |
| `4` | The cost or token budget was exceeded. |

## Errors and redaction

Errors go to stderr as one `error: <message>` line. Every value resolved through mamori is
replaced by `‹secret:NAME›` in errors, logs and output, in its plain, base64, URL-encoded and
JSON-escaped forms. See [Security](/docs/security/).
