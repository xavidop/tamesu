---
layout: ../../layouts/DocsLayout.astro
title: Reports
---

# Reports

Every `tamesu run` writes a run directory and feeds the reporters named in `reporters:` (or
`--reporter`): `console`, `json`, `junit` and `html`.

## The run directory

```text
.tamesu/runs/2026-10-31T14-20-00/
  summary.json
  junit.xml
  report.html          (html reporter, or tamesu report <run-dir>)
  tests/<test-slug>/run-<n>/
    transcript.json      the turns, the tool calls and why the conversation stopped
    result.json          everything about the run: assertions with reasons, metrics, trace ids, events, steps
    audio.wav            (voice audio mode and sip: left the caller, right the agent)
    steps.json           (browser: each step with its outcome, actions and screenshot)
    screenshots/*.png    (browser: one per step, plus screenshot actions)
    trace.har            (browser)
    console.log          (browser: console errors and exceptions)
```

`--out` picks another directory. The slug is the test name lowercased with punctuation as
dashes; a second test with the same name gets `-2`.

### result.json

```json
{
  "index": 1,
  "passed": false,
  "assertions": [
    { "name": "judge \"...\"", "pass": false, "reason": "the agent promised an exception", "evidence": ["Sure, 40 days should be fine"], "trace_id": "9b1c..." },
    { "name": "metric turn_latency_ms_p95 < 4000", "pass": true, "reason": "turn_latency_ms_p95 = 1240, want < 4000" }
  ],
  "metrics": { "turn_latency_ms": 1100, "turn_latency_ms_p50": 1050, "turn_latency_ms_p95": 1240, "turns": 3, "llm_cost_usd": 0.0021, "llm_tokens": 4120 },
  "transcript": [ { "role": "user", "text": "...", "at": "..." }, { "role": "agent", "text": "...", "index": 1 } ],
  "tool_calls": [ { "Name": "lookup_policy", "Args": { "topic": "returns" } } ],
  "trace_ids": ["3f2a...", "9b1c..."],
  "stop_reason": "simulator: the agent answered the question",
  "usage": { "calls": 3, "input_tokens": 3900, "output_tokens": 220, "cost_usd": 0.0021 }
}
```

`error` and `error_kind` (`timeout`, `infra`, `budget`, `cancelled`) say why a run did not
complete. `trace_ids` are the Genkit traces of the simulator and judge calls, which the Genkit
Developer UI shows when `genkit.dev_ui` is on (see [AI](/docs/ai/#the-developer-ui)).

## console

Live progress on stdout: a header, one `PASS`/`FAIL`/`ERROR` line per test with its duration
(and, with `repeat`, how many runs passed against the threshold), and on failure the failing
assertions' reasons and the last three turns. `-v` adds a `START` line per test and a line per
run. The footer counts tests, time, model calls, tokens and cost.

```text
tamesu run: 2 tests, output in .tamesu/runs/2026-10-31T14-20-00
PASS   support-agent / refund policy question  2.1s
FAIL   support-agent / reschedule booking by voice  14.8s  (3/5 runs passed, threshold 80%)
    run 2:
      x judge "The agent confirmed the new date": the agent proposed Sunday instead of Saturday
      last turns:
        user:  Saturday, please.
        agent: Done, I moved it to Sunday the 8th.

1 passed, 1 failed, 0 errors in 17.2s · 14 model calls, 21034 tokens, $0.0112
```

## junit

`junit.xml`, one `testsuite` per suite and one `testcase` per test, repeats aggregated: the
failure message carries the pass rate or the first failing assertion, the body every failing
run's reasons, and `system-out` the first failing transcript. An infrastructure error is an
`error`, not a `failure`. GitHub Actions, GitLab and Jenkins test reporters read it as is.

## json

`summary.json`, the whole invocation: every test with every run (`result.json` inlined), in
load mode a `load` profile per test (users, runs, runs per second, error rate, turn latency
percentiles; see [CI: load mode](/docs/ci/#load-mode)), the
totals and the model usage. It is always written, whatever `--reporter` says, so a script can
read one file.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | every test passed |
| `1` | a test failed (an assertion, a timeout, or a pass rate under its threshold) |
| `2` | config or schema error |
| `3` | an infrastructure error on any test |
| `4` | the cost budget stopped the run |

## html

`report.html` is one self-contained file (no external assets): the totals, then every test
with its runs: the assertions and their reasons and evidence, the transcript (or the browser
steps), the metrics, the event timeline, a strip of step screenshots, an audio player for
`audio.wav` with a button per caller and agent utterance (and DTMF) that seeks to that moment,
the artifacts and the Genkit trace ids. Artifacts are linked relative to the file, so the run
directory can be zipped and shared. `tamesu report <run-dir>` rebuilds it from `summary.json`
(for example on a CI artifact).
