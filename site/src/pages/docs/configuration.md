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
  plugins: [googlegenai, anthropic, openai, ollama, deepgram, elevenlabs]   # only these are initialised
  dev_ui: false                             # true: export traces to the Genkit Developer UI
models:                                     # every value is a Genkit model reference
  default: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-5
  browser_agent: googleai/gemini-2.5-pro
  simulator: openai/gpt-4.1-mini
  scenario_generator: googleai/gemini-2.5-pro
speech:                                     # audio mode and sip; still Genkit models
  stt: { model: deepgram/nova-3, language: en }          # or googleai/gemini-2.5-flash (audio input)
  tts: { model: googleai/gemini-2.5-flash-preview-tts }  # or elevenlabs/eleven_flash_v2_5, openai/gpt-4o-mini-tts
  vad: { provider: energy }                 # energy (default) or silero (needs a build with -tags onnx)
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
  allowed_numbers: ["+1800555*", "+3491*"]  # E.164 patterns tamesu may dial
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
| `genkit.plugins` | none | Genkit plugins to initialise: `googlegenai`, `anthropic`, `openai`, `ollama`, `deepgram`, `elevenlabs`, `evaluators`. Credentials come from the secrets with the conventional names below. |
| `genkit.dev_ui` | `false` | Export traces to the Genkit Developer UI. |
| `models.*` | `default: googleai/gemini-2.5-flash` | The model for each job: `default`, `judge`, `browser_agent`, `simulator`, `scenario_generator`. `models.credentials` maps a plugin to `{{secrets.X}}` when the secret is not named conventionally. |
| `speech.stt`, `speech.tts`, `speech.vad` | `vad: energy` | Speech models (Genkit references, with `language` and `voice` defaults) and the voice activity detector, for audio mode and sip. See [AI](/docs/ai/) for the speech models. |
| `sip.*` | `max_concurrent_calls: 2`, `inbound_room: tamesu-sip-inbound` | The LiveKit SIP service, trunk ids and number, concurrency, the numbers tamesu may dial and the `trunk` block `tamesu sip setup` creates trunks from. See [SIP](/docs/targets/sip/) and [SIP setup](/docs/sip-setup/). |
| `defaults.*` | `timeout: 3m`, `repeat: 1`, `retries: 0`, `pass_threshold: 1.0`, `max_goal_steps: 15` | Per-test defaults; a suite's `defaults:` and a test's own keys override them. `max_goal_steps` caps the browser agent's actions per goal step. |
| `budgets.*` | `max_cost_usd: 5`, `max_tokens: 2_000_000` | Caps on tamesu's own model spend per run (exit code 4 when exceeded). |
| `livekit.max_concurrent_rooms` | `4` | How many LiveKit tests run at once. |
| `reporters` | `[console, junit, html]` | Reporters to run: `console`, `junit`, `json`, `html`. |
| `secrets` | none | Secret name to mamori ref. |
| `vars` | none | Project-wide values for `{{vars.x}}` and `${VAR}` in secret refs. |

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
provider that fails is an infrastructure error (exit 3). Values are held as mamori
`secret.String` and revealed only at the moment of use: an HTTP header, a LiveKit token
signature, a Genkit plugin's initialisation, a value typed into a browser field.

**Using.** `{{secrets.NAME}}` anywhere a string is accepted, such as
`api_key: "{{secrets.LIVEKIT_API_KEY}}"` or `Authorization: "Bearer {{secrets.API_TOKEN}}"`.
`${VAR}` is for non-secret values; `tamesu validate` warns when it appears in `api_key`,
`api_secret`, `headers.Authorization`, `password` or `token`.

**Model credentials.** Genkit plugins are initialised from the resolved secrets by conventional
name (`GEMINI_API_KEY` or `GOOGLE_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`DEEPGRAM_API_KEY`, `ELEVENLABS_API_KEY`), or from `models.credentials: { googleai:
"{{secrets.X}}" }`.

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
  `{{faker.email}}` (also `faker.name`, `faker.phone`, `faker.uuid`) are rendered when a value is
  used. A REST `request.body` also takes `{{session}}` and `{{message}}`; a SIP inbound trigger
  takes `{{sip.inbound_number}}`.

## Suites override the project

A suite may carry its own `defaults:`, `models:`, `budgets:` and `secrets:`; non-empty keys win
over the project's. A test overrides `repeat`, `pass_threshold`, `retries`, `timeout`, `seed`
and `mode` for itself.

## Where the file lives

`tamesu` looks for `tamesu.yaml` in the working directory; `--config path/to/tamesu.yaml` names
another. The `suites:` globs are relative to the file, so running from a subdirectory works.
