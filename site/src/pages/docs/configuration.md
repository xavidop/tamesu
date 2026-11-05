---
layout: ../../layouts/DocsLayout.astro
title: Configuration
---

# Configuration

`tamesu.yaml` holds project-level defaults. Suites can override `defaults`, `models`, `budgets`
and `secrets`. The file is optional: without it, the defaults below apply. Its JSON Schema is
[`schema/tamesu-config.schema.json`](https://github.com/xavidop/tamesu/blob/main/schema/tamesu-config.schema.json).

## The whole file

```yaml
version: 1
suites: ["tests/**/*.yaml"]               # what validate, list (and the runner) use when no paths are given
genkit:
  plugins: [googlegenai, anthropic, openai, ollama, deepgram, elevenlabs, cartesia, soniox, assemblyai, azure, rime, googlespeech, speechmatics, gladia]   # only these are initialised
  dev_ui: false                             # true: export traces to the Genkit Developer UI
models:                                     # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
  browser_agent: googleai/gemini-2.5-pro
  simulator: openai/gpt-4.1-mini
  scenario_generator: googleai/gemini-2.5-pro
  embedder: googleai/text-embedding-004     # similar_to assertions; default: your provider's usual embedder
  fallback:                                 # models tried in order when a job's model keeps failing
    default: [openai/gpt-4.1-mini]          # for every job without a list of its own
    judge: [googleai/gemini-2.5-pro]
  retry: { attempts: 3, backoff: 500ms, max_backoff: 10s }   # per model, on rate limits, outages and timeouts
speech:                                     # audio mode and sip; still Genkit models
  stt: { model: deepgram/nova-3, language: en, fallback: [googleai/gemini-2.5-flash] }
  tts: { model: googleai/gemini-2.5-flash-preview-tts, fallback: [elevenlabs/eleven_flash_v2_5] }
  vad: { provider: energy }                 # energy (default) or silero (the neural detector; needs onnxruntime)
sip:
  livekit_url: ${LIVEKIT_URL}               # LiveKit server with the SIP service enabled
  outbound_trunk_id: ST_xxxxxxxx            # printed by tamesu sip setup
  inbound_number: "+34910000000"            # number your trunk routes to tamesu (for testing outbound agents)
  allowed_numbers: ["+1800555*"]            # what tamesu may dial; required when a target's to is a template
  max_concurrent_calls: 2
  trunk:                                    # the trunk you bring, for tamesu sip setup
    address: sip.twilio.com
    numbers: ["+34910000000"]
    auth_username: "{{secrets.SIP_USER}}"
    auth_password: "{{secrets.SIP_PASSWORD}}"
defaults:
  timeout: 3m
  repeat: 1
  retries: 0
  pass_threshold: 1.0                       # fraction of repeats that must pass
budgets:
  max_cost_usd: 5
  max_tokens: 2_000_000
livekit:
  max_concurrent_rooms: 4
reporters: [console, junit, html]
secrets:                                    # resolved with mamori, see below
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ANTHROPIC_API_KEY: env:ANTHROPIC_API_KEY,aws-sm://ci/tamesu#anthropic
  DEEPGRAM_API_KEY: vault://secret/data/tamesu#deepgram
  ELEVENLABS_API_KEY: op://CI/elevenlabs/credential
```

## Sections

| Key | Default | Meaning |
|---|---|---|
| `version` | `1` | Format version; always 1. |
| `suites` | `["tests/**/*.yaml"]` | Globs (`**` allowed) of suite files, relative to `tamesu.yaml`. Paths on the command line win. |
| `genkit.plugins` | none | Genkit plugins to initialise: `googlegenai`, `anthropic`, `openai`, `ollama`, `deepgram`, `elevenlabs`, `cartesia`, `soniox`, `assemblyai`, `azure`, `rime`, `googlespeech`, `speechmatics`, `gladia`, `evaluators`. Credentials come from the secrets with the conventional names below. |
| `genkit.dev_ui` | `false` | Export traces to the Genkit Developer UI. |
| `models.*` | `default: googleai/gemini-2.5-flash` | The model for each job: `default`, `judge`, `browser_agent`, `simulator`, `scenario_generator`, and `embedder` for `similar_to` assertions. `models.fallback` and `models.retry` are the [retries and fallbacks](/docs/ai/#retries-and-fallbacks). `models.credentials` maps a plugin to `{{secrets.X}}` when the secret is not named conventionally. |
| `speech.stt`, `speech.tts`, `speech.vad` | `vad: energy` | Speech models (Genkit references, with `language` and `voice` defaults) and the voice activity detector, for audio mode and sip. See [AI](/docs/ai/) for the speech models. |
| `sip.*` | `max_concurrent_calls: 2`, `inbound_room: tamesu-sip-inbound` | The LiveKit SIP service, trunk ids and number, concurrency, the numbers tamesu may dial and the `trunk` block `tamesu sip setup` creates trunks from. See [SIP](/docs/targets/sip/) and [SIP setup](/docs/sip-setup/). |
| `defaults.*` | `timeout: 3m`, `repeat: 1`, `retries: 0`, `pass_threshold: 1.0`, `max_goal_steps: 15` | Per-test defaults; a suite's `defaults:` and a test's own keys override them. `max_goal_steps` caps the browser agent's actions per goal step. |
| `budgets.*` | `max_cost_usd: 5`, `max_tokens: 2_000_000` | Caps on tamesu's own model spend per run (exit code 4 when exceeded). |
| `livekit.max_concurrent_rooms` | `4` | How many LiveKit tests run at once. |
| `cache.dir` | `.tamesu/cache` | Where the caches live: browser action plans (see [Browser](/docs/targets/browser/)) and cached simulator turns and judge verdicts (see [Replay](/docs/ai/#replay-cached-turns-and-verdicts)). |
| `baseline.dir` | `.tamesu/baseline` | Where `tamesu baseline save` keeps the baseline; `baseline.max_latency_increase_pct` (20), `baseline.max_cost_increase_pct` (50) and `baseline.fail_on_new_failures` (true) decide what `--baseline` and `tamesu compare` call a regression. |
| `cache.llm` | `true` | Answer the simulated user's turns and the judge's verdicts from the cache when the same conversation was seen before; `false` calls the models every time. |
| `labels` | | Key-value labels written into every run's summary, such as `env: staging` or `agent: "${GIT_SHA:-dev}"`; `tamesu run --label` adds or overrides them. |
| `defaults.assert`, `defaults.slo` | | Assertions every test must pass (`defaults/<name>` in the results; packs apply) and maximums per metric (`slo/<metric>`; a metric a test does not measure passes). See [Suite-wide assertions and SLOs](/docs/concepts/#suite-wide-assertions-and-slos). |
| `reporters` | `[console, junit, html]` | Reporters to run: `console`, `junit`, `json`, `html`, `markdown`, `github`, `slack`, `sarif` (see [Reports](/docs/reports/)). |
| `slack.webhook` | | The incoming webhook of the `slack` reporter, as `{{secrets.NAME}}`; `slack.only_failures` posts only on failures or a regression; `slack.link` goes at the end of the message. |
| `secrets` | none | Secret name to mamori ref. |
| `vars` | none | Project-wide values for `{{vars.x}}` and `${VAR}` in secret refs. |
| `environments` | none | Named deployments (`staging`, `production`): `vars`, `secrets` and `labels` that `tamesu run --env <name>` applies on top of the project's. See [Environments](#environments). |

## Environments

One configuration, several deployments. An environment names the vars, secrets and labels of
one of them; `tamesu run --env staging` applies it on top of the project's, and the run is
labelled `env: staging`.

```yaml
vars: { agent_url: http://localhost:8080/chat }           # the default: a local agent
environments:
  staging:
    vars: { agent_url: https://staging.example.com/chat }
    secrets: { AGENT_TOKEN: env:STAGING_AGENT_TOKEN }
  production:
    vars: { agent_url: https://api.example.com/chat }
    secrets: { AGENT_TOKEN: aws-sm://prod/agent-token }
    labels: { tier: prod }
```

Suites reach the agent through the var (`url: "{{vars.agent_url}}"`), so nothing in a suite
names an environment. The environment's vars win over the project's and the suites'; `--var`
still wins over everything. Its secrets override the project entries of the same name, and are
resolved like any other ([secrets with mamori](#secrets-with-mamori)).

`tamesu run --matrix env=staging,production` runs the selection against both and compares them
in `matrix.md` (see [comparing models and prompts](/docs/cli/#comparing-models-and-prompts));
`tamesu compare` puts two runs side by side after the fact, whichever environments they came
from. A name that is not declared is a configuration error that lists the environments.

## Hooks: setup and teardown

`hooks` run shell commands or HTTP requests around the tests, in `tamesu.yaml` for every suite
and in a suite for its own; a suite's hooks run after the project's. Use them to seed an account,
reset the agent's conversation store, or clean up what a test created.

```yaml
hooks:
  before_all:                                   # once per suite, before its first test
    - command: ./scripts/seed-fixtures.sh
  before_each:                                  # before every run of every test
    - http: { url: "{{vars.admin_url}}/sessions/reset", method: POST, headers: { Authorization: "Bearer {{secrets.ADMIN_TOKEN}}" } }
  after_each:                                   # after every run, whatever its result
    - command: ./scripts/cleanup.sh "$TAMESU_SESSION"
      timeout: 30s
  after_all:
    - command: ./scripts/drop-fixtures.sh
```

A hook is a `command` (run with `sh -c`) or an `http` request (`url`, `method`, `body`,
`headers`); templates render with the suite's vars and secrets. Commands see `TAMESU_SUITE`,
`TAMESU_OUT_DIR` and, around a run, `TAMESU_TEST`, `TAMESU_RUN`, `TAMESU_RUN_DIR` and
`TAMESU_SESSION`. A `before_all` failure stops the run; a `before_each` failure is an
infrastructure error for that run (exit 3); `after_*` failures are logged and change nothing.
Each hook may take `timeout` (default `60s`). Output goes to `hooks.log` in the run directory
(`before_each` / `after_each`) or next to `summary.json` (`before_all` / `after_all`); the
rendered command is never written there, since it may carry a secret.

## Secrets with mamori

All secrets go through [mamori](https://github.com/xavidop/mamori). tamesu never reads an API
key, password or token any other way.

**Declaring.** A `secrets:` map in `tamesu.yaml` and/or in a suite (suite entries override
project entries by name). Each value is a mamori ref:

| Ref | Where it comes from |
|---|---|
| `env:NAME` | the process environment (and `--env-file`, see below) |
| `dotenv://.env#NAME` | a dotenv file |
| `file:///run/secrets/name` | a file |
| `aws-sm://path#key`, `aws-ps://path` | AWS Secrets Manager, Parameter Store |
| `vault://secret/data/x#key` | HashiCorp Vault |
| `gcp-sm://project/name` | Google Secret Manager |
| `azure-kv://vault/name` | Azure Key Vault |
| `k8s-secret://ns/name#key` | a Kubernetes secret |
| `op://vault/item/field` | 1Password |
| `doppler://project/config/name` | Doppler |
| `infisical://project/env/name` | Infisical |
| `sops://file.yaml#key` | a SOPS-encrypted file |

A **precedence chain** `env:X,aws-sm://ci/x#key` tries each link in order; a not-found moves on to
the next, any other failure stops. `#key` selects a field of a JSON secret (a JSON Pointer such as
`#/credentials/user` works too) and `?decode=base64` decodes it. `${VAR}` inside a ref is a **ref
var** fed by `--var KEY=VAL` and the suite's `vars:`, never by the ambient environment, so
`aws-sm://${ENV}/support-agent#api_token` picks the environment you name.

**Resolving.** Once per run, before any test starts, for every secret the selected tests use
(unreferenced secrets are never fetched). A chain with no value is a config error (exit 2); a
provider that fails is an infrastructure error (exit 3). Values are kept redacted and revealed
only at the moment of use: an HTTP header, a LiveKit token signature, a model plugin's
initialisation, a value typed into a browser field.

**Using.** `{{secrets.NAME}}` anywhere a string is accepted, such as
`api_key: "{{secrets.LIVEKIT_API_KEY}}"` or `Authorization: "Bearer {{secrets.API_TOKEN}}"`.
`${VAR}` is for non-secret values; `tamesu validate` warns when it appears in `api_key`,
`api_secret`, `headers.Authorization`, `password` or `token`.

**Model credentials.** Genkit plugins are initialised from the resolved secrets by conventional
name (`GEMINI_API_KEY` or `GOOGLE_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`DEEPGRAM_API_KEY`, `ELEVENLABS_API_KEY`, `CARTESIA_API_KEY`, `SONIOX_API_KEY`,
`ASSEMBLYAI_API_KEY`, `AZURE_SPEECH_KEY` with `AZURE_SPEECH_REGION`, `RIME_API_KEY`,
`GOOGLE_CLOUD_API_KEY` with `GOOGLE_CLOUD_PROJECT` for speech to text, `SPEECHMATICS_API_KEY`,
`GLADIA_API_KEY`), or from
`models.credentials: { googleai: "{{secrets.X}}" }`.

**`--env-file .env`** maps to mamori's dotenv provider: an `env:` ref that is not set in the
process environment is looked up in the file, and so is a `${VAR}`. The process environment is
never mutated.

**`tamesu doctor`** resolves every declared secret and reports which link of each chain
answered, without printing a value. See [Security](/docs/security/) for redaction.

## Interpolation and templates

- `${VAR}` and `${VAR:-default}` expand from the environment (and `--env-file`) everywhere except
  inside `secrets:` refs. `tamesu validate` treats an unset variable as a warning and leaves the
  placeholder, so suites can be checked on a machine without the variables; `doctor` (and the
  runner) treat it as an error. `$${X}` is the literal `${X}`.
- `{{uuid}}`, `{{now}}` (RFC 3339), `{{env.X}}`, `{{vars.x}}`, `{{secrets.X}}` and
  `{{faker.email}}` (also `faker.name`, `faker.phone`, `faker.uuid`, `faker.number`, a nine-digit
  integer) are rendered when a value is used. A REST `request.body` also takes `{{session}}` and
  `{{message}}`, and `{{session|number}}` renders as a JSON number for APIs with integer ids; a
  SIP inbound trigger takes `{{sip.inbound_number}}`.

## Reaching the agent

Every target but `browser` retries its connection to the agent: joining the websocket or the
LiveKit room when a session starts, and for `rest` and `grpc` each request that never reached the
agent (refused, reset, timed out) or that came back 502, 503 or 504. Three attempts with a 500ms
backoff that doubles, up to 5s, is the default; `connect:` on the target changes it:

```yaml
targets:
  api:
    type: rest
    url: https://staging.example.com/chat
    connect: { attempts: 5, backoff: 1s, max_backoff: 10s }   # attempts: 1 retries nothing
```

A refused credential or a bad request (a 4xx handshake, an `Unauthenticated` RPC) is not retried.
Every retry is a warning on stderr; when the attempts run out the error names their count, and
the test is an infrastructure error (exit 3) that a test-level `retries` can re-run. This is
separate from the model retries above: those cover tamesu's own model calls, this covers your
agent.

## Suites override the project

A suite may carry its own `defaults:`, `models:`, `budgets:` and `secrets:`; non-empty keys win
over the project's. A test overrides `repeat`, `pass_threshold`, `retries`, `timeout`, `seed`
and `mode` for itself.

## Where the file lives

`tamesu` looks for `tamesu.yaml` in the working directory; `--config path/to/tamesu.yaml` names
another. The `suites:` globs are relative to the file, so running from a subdirectory works.
