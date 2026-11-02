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

## Reference answers

When the right answer is known, compare against it instead of describing it: the `golden`
assertion holds the agent's turns against reference turns, from a file next to the suite or
inline. `mode: exact` wants the same words (case, punctuation and spacing aside), `similar` a
word overlap of at least `min` (default 0.8), and `judge` asks the judge whether each turn means
the same as its reference, which tolerates rewording but not a changed fact.

```yaml
assert:
  - golden: { file: golden/refund.json, mode: judge }
  - golden: { turns: ["Our return window is 30 days from delivery."], mode: similar, min: 0.7 }
```

The file is a JSON array of agent turns, or a `transcript.json` from an earlier run. The first
run writes a missing file from its transcript (and tells you to review it); `tamesu run
--update-golden` rewrites every golden file from the current run once the agent's new answers
are the ones you want.

## Calibrating the judge

A rubric is only as good as the judge's reading of it. Before gating a release on one, measure
the judge against conversations a person has already marked: `tamesu judge calibrate` runs
`models.judge` over labelled transcripts and reports how often they agree, per rubric.

```yaml
# labels.yaml
- name: states the policy
  rubric: "The agent states the 30-day return policy and does not promise an exception"
  expected: pass
  transcript:
    - { role: user, text: "Can I return shoes I bought 40 days ago?" }
    - { role: agent, text: "Our return window is 30 days, so I'm afraid not." }
- name: invents an exception
  rubric: "The agent states the 30-day return policy and does not promise an exception"
  expected: fail
  transcript:
    - { role: user, text: "Can I return shoes I bought 40 days ago?" }
    - { role: agent, text: "Sure, send them back whenever you like." }
- name: warm tone
  rubric: "The tone stays warm"
  scale: 1-5
  min: 4
  expected: pass
  transcript:
    - { role: user, text: "Hi" }
    - { role: agent, text: "Hello! Lovely to hear from you." }
```

```sh
tamesu judge calibrate labels.yaml
tamesu judge calibrate labels.yaml --min-agreement 0.9 --out calibration.json
```

```text
rubric                                                              labels  agree  false pass  false fail  errors  agreement
The tone stays warm                                                 1       0      0           1           0       0%
The agent states the 30-day return policy and does not promise …    2       2      0           0           0       100%

agreement 67% (2 of 3 labels), 0 false pass, 1 false fail, 0 errors · minimum 80%: BELOW THE MINIMUM

disagreements:
  x warm tone: expected pass, judge said fail · The greeting is friendly but short …
```

A false pass is a transcript a person failed and the judge passed: the rubric is too loose, or
the judge too lenient. A false fail is the opposite. Rewrite the weakest rubrics (the table puts
them first), add the cases the judge got wrong as labels, and run again; verdicts are cached
like a run's, so only new or changed labels call the model. Each label takes the same fields as
a `judge` assertion (`scale`, `min`) and may carry `tool_calls` when the rubric is about tools.
Exit `1` when the agreement is under `--min-agreement` (default 0.8), so a pipeline can keep
the judge honest alongside the agent.

## Cost

Every judge call is counted: `llm_cost_usd` and `llm_tokens` are metrics of each run, the
summary totals them, and `budgets.max_cost_usd` stops the run when exceeded (exit code 4). Prices
come from the built-in price list; an unknown model is counted in tokens and priced at zero.

## Without a model

A binary built with `-tags llmfake` provides `fake/judge`, which passes every rubric unless it
contains `FAIL`. It checks your suites and setup, not the agent; run the real judge on the suites
that matter.
