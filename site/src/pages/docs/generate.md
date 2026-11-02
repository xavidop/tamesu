---
layout: ../../layouts/DocsLayout.astro
title: Generating tests
---

# Generating tests

`tamesu generate` drafts a suite from what you already have: a PRD, the agent's system prompt or
a knowledge base. A model reads the document and proposes tests a careful QA engineer would
start with; you review them, point the target stub at your agent and run.

```sh
tamesu generate --from docs/PRD.md --target api --out tests/drafted.yaml
tamesu generate --from prompts/system.md --target voice --type livekit --count 8
tamesu generate --from docs/shop.md --target web --type browser
```

## What you get

For a conversational target (`rest`, `livekit`, `sip`), each draft test has a persona (name,
style, language), a scenario (`goal`, `max_turns`, sometimes `stop_when`) and assertions: judge
rubrics written to be decidable from the transcript, `contains` for wording the document fixes,
`not_contains` for what the agent must never say, plus a `turns` cap. For a `browser` target,
each test is a list of `goal` steps followed by `expect` steps, with `{{vars.user}}` and
`{{secrets.TEST_PASSWORD}}` instead of credentials.

The suite starts with a target stub for `--type`: for `rest`, `url: ${AGENT_URL}` with the
usual `request.body` and `response` mapping; for `livekit`, the server URL and the key secrets;
for `sip`, an outbound call to a placeholder number; for `browser`, `url: ${APP_URL}`. The draft
is validated like `tamesu validate` would: exit `0` when it passes, `1` when it needs fixing
(the file is kept).

```yaml
# Drafted by tamesu generate from PRD.md: review every test, then run it.
suite: prd
targets:
  api:
    type: rest
    url: "${AGENT_URL}"
    ...
tests:
  - name: refund after the window
    target: api
    tags: [returns, policy]
    persona: {name: Jordi, style: impatient, language: en}
    scenario:
      goal: You bought shoes 40 days ago and insist on a refund
      max_turns: 8
      stop_when: the agent offered the voucher or refused twice
    assert:
      - judge: The agent refuses the refund, does not promise an exception and offers the 10% voucher
      - not_contains: I'm just an AI
      - turns: {lte: 8}
```

## The model

The draft is one model call on `models.scenario_generator` (else `models.default`) with the
`scenario_gen` prompt, which you can override from `.tamesu/prompts/scenario_gen.prompt` like
the others; the document is sent as is (capped at 60k characters) and the call is counted and
priced like any other. With the fake provider, `fake/generate` returns a fixed draft.

## Review before you run

Drafts are a starting point: the model only knows the document. Read every rubric for
decidability, delete what the document does not support, add the details only you know (a
test order number, the real agent URL, a storage state for a logged-in browser), and keep the
suite under version control next to the document it came from.
