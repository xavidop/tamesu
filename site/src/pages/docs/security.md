---
layout: ../../layouts/DocsLayout.astro
title: Security
---

# Security

This page is the security model of tamesu: how secrets are handled, what is redacted, what the
guards around the browser agent and phone calls do, and what none of it covers. To report a
vulnerability, use GitHub's private reporting:
[Report a vulnerability](https://github.com/xavidop/tamesu/security/advisories/new). Please do
not open a public issue; see
[SECURITY.md](https://github.com/xavidop/tamesu/blob/main/SECURITY.md) for the process.

## Secrets are references

`secrets:` entries are [mamori](https://github.com/xavidop/mamori) refs (`env:`, `dotenv://`,
`file://`, `aws-sm://`, `aws-ps://`, `vault://`, `gcp-sm://`, `azure-kv://`, `k8s-secret://`,
`op://`, `doppler://`, `infisical://`, `sops://`), never values. They are resolved once per run,
before any test starts, and only the secrets the selected tests use are fetched. Values are kept
redacted in memory and revealed only at the moment of use: an HTTP header, a LiveKit token
signature, a model plugin's initialisation, a value typed into a browser field.

`tamesu doctor` reports which link of a chain resolved and from which provider, never the value.
A `${VAR}` in a field documented as secret earns a warning from `tamesu validate`, because
`${VAR}` is for non-secret values.

## Redaction

Every resolved value is replaced with `‹secret:NAME›` in everything tamesu writes: transcripts, HARs, logs, reports, error messages and Genkit traces. The
value is matched in its plain, standard and URL-safe base64, URL-encoded and JSON-escaped forms.
As a second line of defence, `${VAR}` values whose name matches `*_KEY`, `*_SECRET`, `*_TOKEN`
or `*PASSWORD*` are redacted too.

Values shorter than three bytes cannot be told apart from ordinary text and are not redacted;
`doctor` warns about them.

## Models never see secrets

The simulated user and the judges receive `{{secrets.NAME}}` placeholders, not values. tamesu
substitutes the real value only inside the transport call (the HTTP request, the CDP `type`
action), after the model has decided what to do.

## What redaction does not cover

- A secret the agent under test echoes back in a paraphrased or re-encoded form tamesu does not
  recognise.
- A secret written into a suite file by hand instead of declared as a ref.
- The memory of the process, and the machine's own logs of the environment.

Treat the run directory like a log file: it holds transcripts of what your agent said.

## The browser agent and `allowed_domains`

The browser agent refuses to navigate outside `allowed_domains` (by default the target URL's
domain). This bounds where a page or a prompt injection can send it; it does not stop a page the
agent is allowed to visit from showing it misleading content, and it does not sandbox Chromium.
Run browser tests against staging, with test accounts.

## Phone calls and `allowed_numbers`

The SIP target refuses to dial a number outside `sip.allowed_numbers` (E.164 patterns such as
`+1800555*`), which is required whenever a number comes from a variable, so a typo cannot call
a stranger. Calls are capped by `sip.max_concurrent_calls` (default 2) and every call has a
`max_call_duration`.

## Budgets

`budgets.max_cost_usd` and `budgets.max_tokens` stop a run that would otherwise spend without
bound (exit code `4`); every model call counts its tokens and estimated cost.

## Out of scope

Vulnerabilities in mamori, Genkit, LiveKit, chromedp or the other dependencies belong upstream;
tamesu updates its dependency once a fix is available. tamesu exercises the agent under test; it
does not protect it.
