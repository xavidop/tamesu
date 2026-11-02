---
layout: ../../layouts/DocsLayout.astro
title: Metrics
---

# Metrics

Every run collects metrics from the transport layer (timestamps around the HTTP call, the audio
frames, the SIP events), never from a model. They appear in `result.json`, in the summary, and
`metric:` assertions compare them.

| Metric | Targets | Definition |
|---|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | rest, livekit, sip, realtime | user turn sent → agent turn complete |
| `time_to_first_token_ms` | rest (sse, websocket), realtime | user turn sent → first delta |
| `time_to_first_audio_ms` | livekit audio, sip, realtime audio | user speech end (VAD, or the audio commit) → agent speech start |
| `interruption_stop_ms` | livekit audio, sip | user starts talking over the agent → agent audio stops |
| `talk_over_ratio` | livekit audio, sip | overlapping speech time / total agent speech time |
| `silence_gap_max_ms` | livekit audio, sip | longest dead air while a reply was expected |
| `call_setup_ms` | sip | dial (or trigger, inbound) → call active |
| `ring_ms` | sip | ringing → answered |
| `greeting_ms` | sip | call active → agent's first speech |
| `call_duration_ms` | sip | active → hangup |
| `stt_word_error_rate` | livekit audio, sip | the agent's transcript of the caller vs. the text tamesu synthesised (only when the agent publishes one) |
| `turns` | conversational | number of agent turns |
| `goal_steps` | browser | agent steps used per goal |
| `llm_cost_usd`, `llm_tokens` | all | tamesu's own model spend during the run |
| `http_requests` | rest | requests of the session |

## Per-turn metrics

A metric measured on every turn (`turn_latency_ms`, `time_to_first_token_ms`) is published as
its mean under the plain name and as `_p50`, `_p95` and `_max`; the percentile is the
nearest-rank value of the run's turns. Assert on `_p95` for a latency budget: one slow turn
should not fail a test, a slow conversation should.

```yaml
- metric: { name: turn_latency_ms_p95, lt: 4000 }
- metric: { name: turn_latency_ms_max, lt: 10000 }
```

## Cost metrics

`llm_cost_usd` and `llm_tokens` are the change of the model meter during the run. They are exact
when runs do not overlap and approximate under `--parallel`, where a concurrent run's calls may
be attributed to a neighbour; the summary's total is always exact.

## Variance

`repeat: 5` runs a test five times and the metrics are recorded per run; `pass_threshold: 0.8`
lets one slow or wrong run through. Compare `result.json` across runs to see the spread.
