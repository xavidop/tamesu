---
layout: ../../layouts/DocsLayout.astro
title: CI
---

# CI

tamesu is built for pipelines: deterministic exit codes, `file:line` errors, secrets that come
from your CI's secret store through mamori rather than from the environment, JUnit output your
CI's test reporter reads, and a Docker image with Chromium preinstalled. This page shows how to
validate, check and run suites in GitHub Actions, GitLab CI and Docker.

## GitHub Actions

```yaml
name: agent tests
on: [push, pull_request]
jobs:
  tamesu:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
      - run: go install github.com/xavidop/tamesu/cmd/tamesu@latest
      - run: tamesu validate --strict
      - run: tamesu doctor --var ENV=ci
        env:
          SUPPORT_API_TOKEN: ${{ secrets.SUPPORT_API_TOKEN }}
          AWS_REGION: eu-west-1
      - run: tamesu run --var ENV=ci --out out
        env:
          SUPPORT_API_TOKEN: ${{ secrets.SUPPORT_API_TOKEN }}
          GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}
      - uses: dorny/test-reporter@v2
        if: always()
        with:
          name: agent tests
          path: out/junit.xml
          reporter: java-junit
```

`validate --strict` fails the job on any warning, including a `${VAR}` used for a secret.
`doctor` resolves every declared secret: with a chain such as
`env:SUPPORT_API_TOKEN,aws-sm://${ENV}/support-agent#api_token`, the job above takes the GitHub
secret, and a workflow without it falls through to AWS Secrets Manager for the `ci` environment
(`--var ENV=ci`), using the runner's AWS credentials.

## GitLab CI

```yaml
tamesu:
  image: ghcr.io/xavidop/tamesu:latest
  script:
    - tamesu validate --strict
    - tamesu doctor --var ENV=ci
  variables:
    SUPPORT_API_TOKEN: $SUPPORT_API_TOKEN
```

The image's working directory is `/work`; GitLab mounts the checkout there when `image:` is the
job's image.

## Docker

```sh
docker run --rm -v "$PWD:/work" -e SUPPORT_API_TOKEN ghcr.io/xavidop/tamesu:latest validate --strict
docker run --rm -v "$PWD:/work" --env-file .env ghcr.io/xavidop/tamesu:latest doctor
```

`ghcr.io/xavidop/tamesu` ships the pure-Go CLI with Chromium preinstalled. Pass secrets as
environment variables or a dotenv file; the image never bakes them in.

## Secrets in CI

Declare secrets as mamori refs and let the pipeline's identity do the rest:

| Store | Ref | What the runner needs |
|---|---|---|
| CI secret variables | `env:NAME` | the variable exported to the job |
| AWS Secrets Manager | `aws-sm://ci/tamesu#anthropic` | AWS credentials (OIDC role or keys) |
| HashiCorp Vault | `vault://secret/data/tamesu#deepgram` | `VAULT_ADDR` and a token |
| 1Password | `op://CI/elevenlabs/credential` | a service account token |
| Google Secret Manager | `gcp-sm://project/name` | application default credentials |
| Azure Key Vault | `azure-kv://vault/name` | a managed identity or a service principal |

A chain `env:NAME,aws-sm://...` lets developers use a local variable and CI the cloud store. See
[Configuration: secrets](/docs/configuration/#secrets-with-mamori).

## Exit codes

| Code | Meaning | In a pipeline |
|---|---|---|
| `0` | passed | green |
| `1` | a test failed | red: read the judge reasons in the report |
| `2` | config or schema error | red: a suite, `tamesu.yaml` or a secret ref needs fixing |
| `3` | infrastructure error | red, but not the agent's fault: a provider, LiveKit or a model was unreachable; retry |
| `4` | budget exceeded | red: raise `budgets.max_cost_usd` or trim the suite |

## Reports

Every run writes a run directory with `summary.json`, `junit.xml` for the CI's test reporter
and, per test and run, `transcript.json` and `result.json` with the assertions, metrics and
judge reasoning. Upload the run directory as an artifact to read a failure after the fact. See
[Reports](/docs/reports/).

## Load mode

`tamesu run --load N` runs every selected test with `N` concurrent virtual users, each looping
runs for `--load-duration` (one run each without it), so a conversational agent can be watched
under modest concurrency with the same suites and assertions: pass/fail still comes from the
assertions and `pass_threshold` over all the runs, and the target caps
(`livekit.max_concurrent_rooms`, `sip.max_concurrent_calls`) still hold, so a phone suite cannot
place more calls than the project allows.

```sh
tamesu run --tags smoke --load 10 --load-duration 2m --max-cost-usd 5
```

Each test's entry in `summary.json` gets a `load` profile: `users`, `runs`, `duration_ms`,
`runs_per_second`, `error_rate` (runs that did not pass) and `turn_latency_ms` (`count`, `p50`,
`p95`, `max` over every agent turn of every run, from transport timestamps). The console prints
the same line under the test and the HTML report shows it next to the runs. Load mode is a
smoke-level tool: the simulator and the judges cost model calls per run, so set a budget.

## Budgets

`budgets.max_cost_usd` (or `--max-cost-usd`) caps what a pipeline run may spend on models and
exits 4 when reached, so a looping agent cannot run up a bill overnight.

## Running without model keys

With the caches committed (`cache.dir`, default `.tamesu/cache`), `tamesu run --replay` plays the
simulated user from cached turns and the browser agent from cached action plans, so a pull
request pipeline needs no model key for the conversations that were recorded; see
[Replay](/docs/ai/#replay-cached-turns-and-verdicts). Record on a trusted branch with the keys
and refresh with `--update-cache` when the scenarios change.

## Gating on a baseline

Save a green run from your main branch as the baseline (`tamesu baseline save <run-dir>`, kept
as an artifact or committed) and run pull requests with `tamesu run --baseline`: a test that
starts failing, or a p95 latency or cost that grows beyond the `baseline:` limits, fails the job
with exit 1 even when every assertion passed. See
[Comparing with a baseline](/docs/reports/#comparing-with-a-baseline).
