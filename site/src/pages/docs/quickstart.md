---
layout: ../../layouts/DocsLayout.astro
title: Quick start
---

# Quick start

This page sets up a project, writes a suite for a REST agent and a voice agent, validates it and
checks the setup with `tamesu doctor`. You need a `tamesu` binary (see [Install](/docs/install/)).

## Scaffold a project

```sh
mkdir support-tests && cd support-tests
tamesu init
```

```text
created tamesu.yaml
created examples/README.md
created examples/support-agent.yaml

Next: edit tamesu.yaml and examples/, then run `tamesu validate` and `tamesu doctor`.
```

`tamesu.yaml` holds the project defaults: which suites to use, the Genkit plugins and models,
timeouts, budgets, reporters and the project-wide secret refs. `examples/support-agent.yaml` is a
suite with two targets, a REST agent and a LiveKit voice agent, and one test for each.

## Read the suite

```yaml
suite: support-agent
tags: [smoke]
vars:
  user: qa@example.com

secrets:                                    # mamori refs, never values
  API_TOKEN: env:SUPPORT_API_TOKEN
  LIVEKIT_API_KEY: env:LIVEKIT_API_KEY
  LIVEKIT_API_SECRET: env:LIVEKIT_API_SECRET

targets:
  api:
    type: rest
    url: ${AGENT_URL:-http://localhost:8080/chat}
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request:
      body: { session_id: "{{session}}", message: "{{message}}" }
    response:
      text: $.reply
      tool_calls: $.tool_calls[*].name
      done: $.end_session

  voice:
    type: livekit
    url: ${LIVEKIT_URL:-ws://localhost:7880}
    api_key: "{{secrets.LIVEKIT_API_KEY}}"
    api_secret: "{{secrets.LIVEKIT_API_SECRET}}"
    room: "support-{{uuid}}"
    mode: text

tests:
  - name: refund policy question
    target: api
    persona: { name: Marta, style: polite, slightly confused, language: en }
    scenario:
      goal: "Find out if you can return shoes bought 40 days ago"
      max_turns: 8
      stop_when: "the agent has given a clear yes/no answer"
    assert:
      - judge: "The agent states the return policy and does not promise an exception"
      - not_contains: "I'm just an AI"
      - metric: { name: turn_latency_p95_ms, lt: 4000 }
```

Three things to notice:

- **Secrets are references.** `env:SUPPORT_API_TOKEN` tells mamori where to find the token; the
  suite uses it as `{{secrets.API_TOKEN}}`. A chain such as
  `env:SUPPORT_API_TOKEN,aws-sm://ci/support#token` tries each link in order.
- **`${VAR:-default}` is for non-secret values.** The agent URL falls back to localhost so the
  suite validates anywhere. `tamesu validate` warns if you use `${VAR}` for a key.
- **Assertions mix kinds.** A `judge` rubric graded by a model, a deterministic `not_contains`,
  and a `metric` on the p95 turn latency. See [Concepts](/docs/concepts/#assertions).

## Validate

```sh
tamesu validate
```

```text
1 suite, 2 tests, 0 errors, 0 warnings
```

Break something to see how errors look. Point the test at a target that does not exist:

```sh
sed -i 's/target: api/target: nope/' examples/support-agent.yaml
tamesu validate; echo "exit $?"
```

```text
examples/support-agent.yaml:40: tests[0] "refund policy question".target: target "nope" is not declared in targets (have api, voice)
1 suite, 2 tests, 1 error, 0 warnings
exit 2
```

Every problem names the file, the line and the path to the key. Exit code `2` means a config or
schema error. Put it back with `git checkout` or `sed -i 's/target: nope/target: api/'`.

## List the tests

```sh
tamesu list
tamesu list --json --filter refund
```

```text
SUITE          TEST                         TARGET  TYPE     TAGS
support-agent  refund policy question       api     rest     smoke
support-agent  reschedule booking by voice  voice   livekit  smoke
```

## Check the setup

`tamesu doctor` validates everything, resolves every declared secret through mamori and tells
you which link of each chain answered, never the value:

```sh
export GEMINI_API_KEY=... SUPPORT_API_TOKEN=... LIVEKIT_API_KEY=... LIVEKIT_API_SECRET=...
tamesu doctor
```

```text
ok   config: tamesu.yaml (version 1)
ok   suites: 1 file, 2 tests
ok   secret API_TOKEN: env (link 1 of 1)
ok   secret GEMINI_API_KEY: env (link 1 of 1)
ok   secret LIVEKIT_API_KEY: env (link 1 of 1)
ok   secret LIVEKIT_API_SECRET: env (link 1 of 1)
skip models: not checked: the module that knows how to is not in this build
skip chromium: not checked: the module that knows how to is not in this build
skip livekit: not checked: the module that knows how to is not in this build
```

Unset a variable and the matching line becomes `fail secret ...: not found in any of 1 ref(s)`
with exit code `2`; a secret backend that is down is `fail ... provider ... failed (unavailable)`
with exit code `3`. Keep secrets out of the shell history with a `.env` file and `--env-file .env`:
mamori reads it, the process environment is never mutated.

## Running the tests

Running a suite is the next phase of tamesu: the runner, the Genkit layer and the REST target
are planned (`tamesu run` is not in the CLI yet; see the [roadmap](/docs/roadmap/)). The schema,
the validator, the secrets layer and `doctor` you just used are what every run will build on.

## Next

- [Suites](/docs/suites/): every key of the YAML, generated from the schema.
- [Configuration](/docs/configuration/): `tamesu.yaml`, secrets with mamori, interpolation.
- [CLI reference](/docs/cli/): every command, flag and exit code.
- [CI](/docs/ci/): validate and doctor in GitHub Actions, GitLab and Docker.
