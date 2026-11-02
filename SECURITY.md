# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities **privately**. Do not open a public issue, discussion, or
pull request for a security problem.

Use GitHub's private vulnerability reporting:
**[Report a vulnerability](https://github.com/xavidop/tamesu/security/advisories/new)**. This opens
an advisory visible only to you and the maintainers.

Include as much as you can:

- A description of the issue and its impact.
- Steps to reproduce, or a proof of concept.
- The affected module(s) and version(s).
- Any suggested remediation.

Please do not include real API keys, tokens or transcripts with personal data in your report.

## What to expect

- Acknowledgement within 3 business days.
- An initial assessment and severity rating within 7 days.
- Coordinated disclosure: we will work with you on a fix and a release, and credit you (if you
  wish) in the advisory and release notes.

## Supported versions

tamesu is a multi-module repository. Security fixes land on the latest release of the affected
module, so upgrade that module to its newest version.

| Module | Supported |
| --- | --- |
| core (`github.com/xavidop/tamesu`) | latest release |
| `cmd/tamesu` (the CLI) | latest release |
| `llm`, `audio`, `targets/*`, `llm/plugins/*` | latest release |

Third-party components the binary bundles or loads are listed in `THIRD_PARTY_NOTICES.md`; a
vulnerability in one of them is handled by updating it in a new release.

## Scope

In scope:

- The core library and the CLI in this repository.
- Secret handling: any way a value resolved through mamori reaches a transcript, log, report,
  artifact, error message, model prompt or Genkit trace unredacted.
- Configuration handling: a suite or `tamesu.yaml` that makes tamesu read a file, call a host or
  dial a number it should not.

Out of scope:

- Vulnerabilities in mamori, Genkit, LiveKit, chromedp or the other dependencies themselves.
  Report those upstream; we will update our dependency once a fix is available.
- Issues that require an already-compromised host, or credentials the reporter already controls.
- The agent under test. tamesu exercises it; it does not protect it.

## Security model

- **Secrets are references.** `secrets:` entries are mamori refs (`env:`, `dotenv://`,
  `aws-sm://`, `vault://`, `gcp-sm://`, `azure-kv://`, `k8s-secret://`, `op://`, `doppler://`,
  `infisical://`, `sops://`), never values. They are resolved once per run, before any test
  starts, held as `secret.String` and revealed only at the moment of use: an HTTP header, a token
  signature, a model plugin's initialisation. `tamesu doctor` reports which link of a chain
  resolved, never the value.
- **Redaction.** Every resolved value is registered with the redactor, which replaces it (and its
  base64, URL-encoded and JSON-escaped forms) with `‹secret:NAME›` in everything tamesu writes.
  As a second line of defence, `${VAR}` values whose name matches `*_KEY`, `*_SECRET`, `*_TOKEN`
  or `*PASSWORD*` are redacted too. Values shorter than three bytes cannot be told apart from
  ordinary text and are not redacted; `tamesu doctor` warns about them.
- **What redaction does not cover.** A secret the agent under test echoes back in a paraphrased
  or re-encoded form it does not recognise, a secret written into a suite file by hand, and the
  memory of the process. Treat the run directory like a log file.
- **Models never see secrets.** The simulated user and the judges receive `{{secrets.NAME}}`
  placeholders; tamesu substitutes the real value only inside the transport call.
- **The browser agent and `allowed_domains`.** When the browser target is used, the agent refuses
  to navigate outside `allowed_domains` (by default the target URL's domain). This bounds where
  a page or a prompt injection can send it; it does not stop a page the agent is allowed to visit
  from showing it misleading content.
- **Phone calls and `allowed_numbers`.** The SIP target refuses to dial a number outside
  `sip.allowed_numbers`, which is required whenever a number comes from a variable, so a typo
  cannot call a stranger. Calls are capped by `sip.max_concurrent_calls` and `max_call_duration`.
- **Budgets.** `budgets.max_cost_usd` and `budgets.max_tokens` stop a run that would otherwise
  spend without bound (exit code 4).
