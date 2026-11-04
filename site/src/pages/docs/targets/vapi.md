---
layout: ../../../layouts/DocsLayout.astro
title: Vapi target
---

# Vapi target

![A person with headphones and a clipboard listening while a small robot with a headset talks into a phone whose cable runs up into a cloud](/img/target-vapi.jpg)

The `vapi` target tests an assistant hosted on **Vapi** without a phone number: tamesu creates a
call on Vapi's websocket transport, connects to it, speaks the simulated caller's words as raw
PCM and reads the assistant's audio back, together with the control messages Vapi sends on the
same socket (transcripts, speech updates, tool calls, the end of the call). The assistant runs
exactly as it does for a phone or web caller, with its own voice, model and tools.

```yaml
targets:
  support:
    type: vapi
    api_key: "{{secrets.VAPI_API_KEY}}"
    assistant_id: "{{vars.assistant_id}}"           # or assistant: { ... } (transient), or squad_id
    variables: { customer_name: Marta, plan: gold }  # the assistant's variableValues
    assistant_overrides: { firstMessage: "Hi Marta, how can I help?" }
    sample_rate: 16000                               # PCM both ways (default)
    idle: 2s                                         # silence that ends the assistant's turn
    turn_timeout: 60s
tests:
  - name: return after 40 days
    target: support
    persona: { name: Marta, style: "polite, slightly confused", language: en, voice: en-US-female }
    scenario:
      goal: "Find out if the shoes you bought 40 days ago can still be returned"
      max_turns: 6
    assert:
      - judge: "The assistant states the 30-day window and does not promise an exception"
      - tool_called: lookup_policy
      - metric: { name: time_to_first_audio_ms_p95, lt: 2500 }
```

The target needs the speech models (`speech.tts.model`, `speech.stt.model` in `tamesu.yaml`):
the caller's words are synthesised, and the assistant's words come from the final transcripts
Vapi sends or, without them, from tamesu's transcription of its audio.

## How a call runs

1. `POST /call` with the assistant (or squad), the overrides and variables, and
   `transport: { provider: vapi.websocket, audioFormat: { format: pcm_s16le, container: raw,
   sampleRate } }`. The answer's `websocketCallUrl` is dialled.
2. The assistant's first message, when it has one, is read as the greeting (`greeting_ms`), with
   a wait of up to 8 seconds.
3. Each user turn is synthesised, resampled to `sample_rate` and sent as 20 ms binary frames in
   real time, followed by a short silence so the assistant's endpointing sees the end of the
   utterance. DTMF is not carried by the websocket transport and is recorded as an event.
4. The assistant's binary frames are its audio; its JSON messages are read as they come:
   `transcript` (final assistant transcripts become the turn's text, `transcript_source: agent`),
   `tool-calls` and `function-call` (the turn's tool calls), `speech-update`, `user-interrupted`
   (the assistant stopped for the caller), `status-update` with `ended` (the call is over, with
   its `endedReason`), `transfer-update` and `hang`. The turn completes after `idle` without audio.
5. Barge-in follows `behaviors.interrupt_probability`: the caller speaks over the assistant and
   the stop time is measured to its `user-interrupted` message, or to the end of the audio it had
   already queued.
6. Closing the session sends `{"type": "hangup"}` unless the assistant ended the call first.

## Options

| Field | Meaning |
|---|---|
| `api_key` | The Vapi private key, as `{{secrets.NAME}}`. |
| `assistant_id`, `assistant`, `squad_id` | Exactly one: a saved assistant, a transient assistant object (string leaves render templates), or a squad. |
| `assistant_overrides` | The call's `assistantOverrides` (first message, voice, model, tools...). |
| `variables` | The assistant's `variableValues` (templates allowed), merged into the overrides. |
| `metadata` | Stored on the call. |
| `sample_rate` | PCM rate in both directions, default `16000`. |
| `idle` | Silence after the assistant's last audio that ends its turn, default `2s`. |
| `turn_timeout` | How long one turn may take, default `60s`. |
| `transcript_source` | `auto` (default: Vapi's final transcripts when it sends them, else STT), `agent` or `stt`. |
| `base_url` | The API, default `https://api.vapi.ai`. |
| `connect` | How creating the call and connecting are retried, as for every target. |

## Metrics and events

The audio metrics of every voice target: `time_to_first_audio_ms`, `greeting_ms`,
`agent_speech_ms`, `user_speech_ms`, `interruptions`, `interruption_stop_ms`,
`call_duration_ms`, plus `ws_messages_received`. Events: `call` (the call id), `greeting`,
`status`, `ended_reason`, `tool_call`, `user_interrupted`, `interruption`, `transfer`, `hang`,
`dtmf_unsupported`, `hangup`, `closed`. The run keeps a stereo `audio.wav` (caller left,
assistant right), and every control message of a turn is in `result.json` for `json_path`.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) that applies to audio targets:
`judge`, `contains`, `regex`, `similar_to`, `tool_called` (Vapi's tool calls), `grounded` with a
`text` context, `captured`, `golden`, `metric`, `turns`, `http` and `command`, the packs, and
`json_path` over the control messages.

## A phone call instead

To test the same assistant on a real phone number, dial it with the [SIP target](/docs/targets/sip/)
or have it call tamesu back through an inbound `trigger.http` to Vapi's call API.
