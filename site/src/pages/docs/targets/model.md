---
layout: ../../../layouts/DocsLayout.astro
title: Model target
---

# Model target

The `model` target tests a **system prompt and its tools directly against a model**, with no
agent server in between. The simulated user's turns go to the model with your prompt; when the
model calls a tool, tamesu answers from the result you configured and lets it continue; the text
it finally says is the agent's turn. It is where a prompt gets regression-tested before there is
an agent runtime, and the fastest loop while the prompt is still changing: edit the prompt, run
the suite, read the judge.

```yaml
targets:
  prompt:
    type: model
    model: googleai/gemini-2.5-flash          # any Genkit model reference; default models.default
    system_file: prompts/support.md           # or system: "You are ..."
    tools:
      - name: lookup_policy
        description: The policy for a topic
        parameters: { type: object, properties: { topic: { type: string } }, required: [topic] }
        result: { topic: "{{args.topic}}", window_days: 30 }
      - name: end_session
        description: Call when the customer has no more questions
tests:
  - name: refund after 40 days
    target: prompt
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario: { goal: "Find out if you can return shoes bought 40 days ago", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - tool_called: lookup_policy
      - not_contains: "As an AI"
```

The model runs through the provider configured in `tamesu.yaml` (`genkit.plugins` and
`models.*`), with the same credentials, pricing and budget as the simulated user and the judges:
every call the prompt makes is counted in the run's usage and can stop the run at
`budgets.max_cost_usd`. Switching the prompt to another provider is the `model` line.

## Keys

| Key | Meaning |
|---|---|
| `model` | The Genkit model reference the prompt runs on (`googleai/…`, `openai/…`, `anthropic/…`, `ollama/…`). Default `models.default`. |
| `system` | The prompt under test. `{{vars.x}}` and `{{secrets.X}}` render, so one suite can test the prompt with several variable sets through `cases`. |
| `system_file` | A file next to the suite holding the prompt, instead of `system`. |
| `tools` | The tools the model may call: `name`, `description`, `parameters` (a JSON schema of the arguments, an object schema) and `result`, what the call returns (any JSON; string leaves may use `{{args.<key>}}`). A tool without a `result` returns an error object, so a prompt that depends on it shows it. |
| `done_tool` | The tool whose call ends the session (default `end_session`); declare it under `tools` so the model knows about it. |
| `temperature`, `max_tokens` | Sampling temperature (0 to 2) and the most output tokens per call; the provider's defaults when unset. |
| `max_tool_rounds` | The most call-and-answer rounds in one turn before tamesu stops and reports the turn without text (default 5). |

## What a run records

Every tool call is recorded on the turn, so `tool_called`, `tool_not_called` and
`tool_sequence` work as on any target, and appears in the run's events with its arguments. The
metrics are `turns`, `tool_calls`, `agent_input_tokens` and `agent_output_tokens` (the prompt's
own token use, apart from tamesu's), plus the usual `turn_latency_ms` from the time the turn
took. The transcript holds the model's text per turn; `result.json` keeps the raw responses.

## Prompts with variables

Test one prompt under several conditions with `cases`:

```yaml
tests:
  - name: tone by market
    target: prompt
    cases:
      - { name: uk, vars: { market: "UK", currency: "GBP" } }
      - { name: us, vars: { market: "US", currency: "USD" } }
    scenario: { goal: "Ask how much shipping costs", max_turns: 4 }
    assert:
      - judge: "The agent quotes a price in {{vars.currency}}"
```

with `system: "You are the {{vars.market}} support agent. Prices are in {{vars.currency}}."`.

## When to move on

The model target checks the prompt, not the plumbing: no retrieval, no real tool code, no
streaming, no latency of your service. Once the prompt lives in an agent, point a `rest`,
`grpc`, `livekit` or other target at it and keep the same tests; the assertions do not change.
