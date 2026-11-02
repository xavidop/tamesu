---
layout: ../../layouts/DocsLayout.astro
title: Judges
---

# Judges

A judge is an LLM that grades a finished conversation against a rubric you write in plain
language. It is the assertion for everything a regular expression cannot express: "states the
return policy and does not promise an exception", "confirmed the new date and did not invent
availability". Judges answer with structured output only: `pass`, `score`, `reason` and
`evidence` quotes, and every result records the judge's trace id and prompt version, so a drift
in verdicts can be traced to a prompt or a model change.

## Writing a rubric

```yaml
assert:
  - judge: "The agent states the 30-day return policy and does not promise an exception"
  - judge:
      rubric: "The agent confirmed the new date and did not invent availability"
      scale: 1-5
      min: 4
      model: anthropic/claude-sonnet-4-6
```

- Say what the agent must do and must not do, in one or two sentences, as you would brief a
  reviewer. Name the facts that matter (the 30-day window, the new date).
- Prefer one behaviour per judge. Two rubrics give two reasons; one compound rubric gives one.
- `scale: binary` (the default) is pass or fail. `scale: 1-5` asks for a score and passes at
  `min` (default 4); use it for quality ("the explanation is clear and complete").
- `model` overrides `models.judge` for one assertion.

## What the judge sees

The whole transcript (user and agent turns), the tool calls the agent made and the metrics of
the run, formatted as text. It never sees a secret: `{{secrets.X}}` placeholders stay
placeholders in anything a model reads.

## Determinism

The judge prompt runs at temperature 0 when the provider supports it. It is a dotprompt file,
`judge.prompt`, with a `version:` in its front matter. The version
is written to every `result.json` (`prompt_version`), so a change to the rubric wording of the
prompt itself is visible in the results. The simulated user's prompt (`simulator.prompt`) is
versioned the same way.

To change a prompt for your project, copy it to `.tamesu/prompts/<name>.prompt` and edit it; the
file with the same name replaces the built-in one. See [AI](/docs/ai/#prompts).

## Judges as Genkit evaluators

The judge is also registered as a Genkit evaluator, `tamesu/judge`, so it works in any Genkit
tooling that runs evaluators, and the `evaluator:` assertion runs any registered Genkit
evaluator on the run:

```yaml
- evaluator: genkitEval/regex          # the evaluators plugin's deterministic metrics
- evaluator: { name: tamesu/judge, min: 1 }
```

With `genkit.plugins: [evaluators]`, `genkitEval/deep_equal`, `genkitEval/regex` and
`genkitEval/jsonata` are available; a Genkit app that embeds tamesu with `llm.WithGenkit` can
use its own evaluators.

## Cost

Every judge call is counted: `llm_cost_usd` and `llm_tokens` are metrics of each run, the
summary totals them, and `budgets.max_cost_usd` stops the run when exceeded (exit code 4). Prices
come from the built-in price list; an unknown model is counted in tokens and priced at zero.

## Without a model

A binary built with `-tags llmfake` provides `fake/judge`, which passes every rubric unless it
contains `FAIL`. It checks your suites and setup, not the agent; run the real judge on the suites
that matter.
