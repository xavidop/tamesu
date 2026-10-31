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
| [`tamesu doctor`](#tamesu-doctor) | Check the config, suites, secrets, models, Chromium and LiveKit |
| [`tamesu version`](#tamesu-version) | Print the version, commit and build date |

`tamesu completion` prints a shell completion script, and `tamesu help` shows help for any
command. The runner and the other commands of the roadmap are listed on the
[roadmap page](/docs/roadmap/).

## Global flags

| Flag | Meaning |
|---|---|
| `--config <path>` | Project configuration file (default `tamesu.yaml` in the working directory). See [Configuration](/docs/configuration/#where-the-file-lives). |
| `-v, --verbose` | Debug logging to stderr. Logs are redacted like everything else. |
| `--var KEY=VAL` | A ref var for `${VAR}` in secret refs and `{{vars.KEY}}` in suites. Repeatable. Wins over the suite's `vars:`. |
| `--env-file <path>` | A dotenv file read through mamori's dotenv provider when an `env:` secret or a `${VAR}` is not set in the process environment. The environment is never mutated. |

## Suite paths

`validate`, `list` and `doctor` take suite paths and globs as arguments (`**` matches across
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
- **models, chromium, livekit**: provided by the modules that know how to reach them; `skip`
  when this build does not include them.

Exit `0` when nothing failed, `2` when a configuration check failed, `3` when a provider or
service was unreachable. `--var` and `--env-file` apply.

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
| `1` | One or more tests failed. |
| `2` | Config or schema error: an invalid suite or `tamesu.yaml`, a bad flag, an unknown command, a secret ref with no value, no suites matching a path. |
| `3` | Infrastructure error: a secret provider, LiveKit, a browser or a model that could not be reached. |
| `4` | The cost or token budget was exceeded. |

## Errors and redaction

Errors go to stderr as one `error: <message>` line. Every value resolved through mamori is
replaced by `‹secret:NAME›` in errors, logs and output, in its plain, base64, URL-encoded and
JSON-escaped forms. See [Security](/docs/security/).
