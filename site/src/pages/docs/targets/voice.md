---
layout: ../../../layouts/DocsLayout.astro
title: Voice target
---

# Voice target

![A person with headphones and a clipboard listening while a small robot with a headset talks into a phone whose cable runs up into a cloud](/img/target-voice.jpg)

The `voice` target tests a voice agent behind **any websocket that carries audio**: raw PCM or
G.711 frames, binary or base64 inside JSON messages, with the protocol's JSON control messages
read by JSONPath. It is how hosted voice platforms expose their agents (Vapi's websocket
transport, Deepgram Voice Agent and others) and how many in-house
voice stacks work, so one target and a recipe per protocol replace a target per vendor. tamesu
speaks the simulated caller's words through the TTS model, streams them as audio, reads the
agent's audio back, and takes the agent's words from the protocol's transcripts or from the STT
model.

```yaml
targets:
  assistant:                                        # a Vapi assistant; see Recipes for others
    type: voice
    session:
      open:                                         # create the call; its captures render below
        url: https://api.vapi.ai/call
        headers: { Authorization: "Bearer {{secrets.VAPI_API_KEY}}" }
        body:
          assistantId: "{{vars.assistant_id}}"
          transport: { provider: vapi.websocket, audioFormat: { format: pcm_s16le, container: raw, sampleRate: 16000 } }
        capture: { ws_url: $.transport.websocketCallUrl, call_id: $.id }
    url: "{{session.ws_url}}"
    audio: { encoding: pcm16, sample_rate: 16000, frames: binary }
    messages:
      transcript: "$[?(@.type=='transcript' && @.role=='assistant' && @.transcriptType=='final')].transcript"
      done: "$[?(@.type=='status-update' && @.status=='ended')].status"
      interrupted: "$[?(@.type=='user-interrupted')].type"
      tool_calls: "$[?(@.type=='tool-calls')].toolCallList[*]"
      close: { type: hangup }
    idle: 2s
tests:
  - name: return after 40 days
    target: assistant
    persona: { name: Marta, style: "polite, slightly confused", language: en, voice: en-US-female }
    scenario: { goal: "Find out if the shoes you bought 40 days ago can still be returned", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - tool_called: lookup_policy
      - metric: { name: time_to_first_audio_ms_p95, lt: 2500 }
```

The target needs the speech models (`speech.tts.model`, `speech.stt.model` in `tamesu.yaml`).

## How a call runs

1. `session.open`, when set, is one HTTP request made first (create the call, mint a token): its
   `capture` JSONPaths take values from the JSON reply, and they render as `{{session.NAME}}` in
   `url`, `headers` and every message template.
2. tamesu connects to `url` with `headers`, sends `messages.open` when set, and waits up to 8
   seconds for the agent's first words (the greeting, `greeting_ms`).
3. Each user turn is synthesised, resampled to `audio.sample_rate`, encoded as `audio.encoding`
   and sent as 20 ms chunks in real time: one binary frame per chunk with `frames: binary`, or
   the `audio.send` template with `{{audio}}` as the base64 chunk with `frames: json`. A short
   silence follows so the agent's endpointing sees the end of the utterance. A `dtmf` step sends
   the `messages.dtmf` template per digit.
4. The agent's audio arrives as binary frames, or inside JSON messages at `audio.receive`. Its
   JSON messages are matched against `messages`: `transcript` yields its words (final
   transcripts, `transcript_source: agent`), `tool_calls` its tool calls, `interrupted` says it
   stopped for the caller, `done` ends the call. The turn completes after `idle` without audio.
5. Barge-in follows `behaviors.interrupt_probability`: the caller speaks over the agent and the
   stop time is measured to the `interrupted` message, or to the end of the audio the agent had
   already queued.
6. Closing the session sends `messages.close` unless the agent ended the call first.

The JSONPaths under `messages` and `audio.receive` run against each incoming message wrapped in
a one-element list, so a filter selects the message and a trailing field reads from it:
`$[?(@.type=='transcript' && @.role=='assistant')].transcript`. Every control message of a
turn is in `result.json` for `json_path`.

## Options

| Field | Meaning |
|---|---|
| `url` | The websocket. Templates render, including `{{session.NAME}}` captures. |
| `headers` | Handshake headers (`Authorization: "Bearer {{secrets.X}}"`). |
| `session.open` | A request made before connecting: `url`, `method` (default `POST`), `headers`, `body`, `capture` (JSONPaths by name into the reply). A 4xx or 5xx fails the session. |
| `audio.encoding` | `pcm16` (default), `mulaw` or `alaw`. |
| `audio.sample_rate` | Hz; default 16000 for `pcm16`, 8000 for G.711. |
| `audio.frames` | `binary` (default) or `json`. |
| `audio.send` | `json` frames: the message template per outgoing chunk; `{{audio}}` is the base64 chunk (default `{ audio: "{{audio}}" }`). |
| `audio.receive` | `json` frames: the JSONPath of the base64 audio in an incoming message. |
| `messages.open` | A message sent once after connecting. |
| `messages.transcript` | JSONPath of the agent's words in a message. Without it the audio is transcribed. |
| `messages.done` | JSONPath truthy on the message that ends the call. |
| `messages.interrupted` | JSONPath truthy on the message that says the agent stopped for the caller. |
| `messages.tool_calls` | JSONPath of the tool calls: names, or objects with `name` and `args` (or `function.name` and `function.arguments`). |
| `messages.dtmf` | A message template per digit, `{{digit}}` rendering. Without it a `dtmf` step is recorded as unsupported. |
| `messages.close` | A message sent before closing (a hangup). |
| `idle` | Silence after the agent's last audio that ends its turn, default `2s`. |
| `turn_timeout` | How long one turn may take, default `60s`. |
| `transcript_source` | `auto` (default: the protocol's transcripts when it sends them, else STT), `agent` or `stt`. |
| `connect` | How `session.open` and the connection are retried, as for every target. |

## Metrics and events

The audio metrics of every voice target: `time_to_first_audio_ms`, `greeting_ms`,
`agent_speech_ms`, `user_speech_ms`, `interruptions`, `interruption_stop_ms`,
`call_duration_ms`, plus `ws_messages_received`. Events: `session_open`, `connected`,
`greeting`, `dtmf` or `dtmf_unsupported`, `tool_call`, `agent_interrupted`, `interruption`,
`interruption_stopped`, `ended`, `close_message`, `closed`. The run keeps a stereo `audio.wav`
(caller left, agent right).

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) that applies to audio targets:
`judge`, `contains`, `regex`, `similar_to`, `tool_called` when the protocol reports tool calls,
`grounded` with a `text` context, `captured`, `golden`, `metric`, `turns`, `http` and `command`,
the packs, and `json_path` over the control messages.

## Recipes

[Recipes](/docs/recipes/) has the configurations for Vapi's websocket transport and Deepgram
Voice Agent. A platform that exposes only a phone number is
dialled with the [SIP target](/docs/targets/sip/), and one that exposes a LiveKit room is joined
with the [LiveKit target](/docs/targets/livekit/).
