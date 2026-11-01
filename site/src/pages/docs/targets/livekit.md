---
layout: ../../../layouts/DocsLayout.astro
title: LiveKit target
---

# LiveKit target

The `livekit` target tests a realtime voice agent by joining its LiveKit room as a simulated
participant, whatever the agent was built with: LiveKit Agents, Voiceflow, Pipecat, a custom
participant. **Text mode** exchanges text streams, so a text-capable agent is tested without
audio: fast, cheap, deterministic. **Audio mode** speaks to the agent and listens to it the way a
caller would: TTS in, VAD and STT out, interruptions, pauses, background noise, a phone line, and
a stereo WAV of every run.

```yaml
targets:
  voice:
    type: livekit
    url: ${LIVEKIT_URL}                           # ws:// or wss://
    api_key: "{{secrets.LIVEKIT_API_KEY}}"
    api_secret: "{{secrets.LIVEKIT_API_SECRET}}"
    room: "support-{{uuid}}"                      # a new room per run
    dispatch: { agent_name: support-agent }       # optional explicit agent dispatch
    agent_identity_prefix: agent-                 # how to recognise the agent participant
    mode: text                                    # text | audio
    data_topic: lk.chat                           # text-mode transport
    join_timeout: 30s
    transcript_source: auto                       # audio: auto | agent | stt
```

## How a turn works

1. tamesu mints a token for `tamesu-sim-<uuid>` that may join and create `room`, with the agent
   dispatch when `dispatch.agent_name` is set, and connects.
2. It waits up to `join_timeout` for the agent participant: a participant of kind `agent`, or
   one whose identity starts with `agent_identity_prefix`. Without a prefix, any remote
   participant counts. No agent is an infrastructure error (exit 3) with a message that says so.
3. Each user turn is sent as a text stream on `data_topic` (`lk.chat`, what LiveKit Agents read
   as text input).
4. The agent's reply is read from its text streams on `lk.transcription` (where LiveKit Agents
   publish their speech as text) and on the data topic, and from transcription events. The turn
   is complete when text arrived and the agent's `lk.agent.state` attribute went
   `thinking`/`speaking` and back to `listening`, or the stream was marked
   `lk.transcription_final`, or no new text arrived for 1.5 s (the fallback for agents that
   publish no state). An agent that leaves ends the session.
5. Tool calls: a custom agent can report them as JSON data packets on the topic
   `tamesu.tool_calls` (`{"name": "...", "args": {...}}` or a list); `tool_called` assertions
   read them.

A greeting the agent sends before the first user turn is attached to the first reply.

## Audio mode

`mode: audio` on the target, a test or `--mode audio` needs `speech.tts` and `speech.stt` in
`tamesu.yaml` (see [AI](/docs/ai/)) and a full build (the lite build has no Opus). A turn then goes:

1. The simulator's text is spoken by the TTS model with the persona's `voice`, resampled to
   48 kHz, run through `behaviors.line_quality` when set, mixed with `behaviors.background_noise`
   at `background_noise_snr_db`, and published as the caller's Opus microphone track, 20 ms at a
   time. Between turns the track carries the noise alone (or silence). A `dtmf` from the
   simulator is sent as real keypad tones.
2. tamesu subscribes to the agent's audio track, decodes it and runs the voice activity
   detector (`speech.vad.provider`: `energy` by default, `silero` in a build with `-tags onnx`).
   `time_to_first_audio_ms` is the caller's speech end to the agent's speech start, from the
   audio itself, never from a model.
3. The turn is complete when the agent stopped speaking and went back to `listening` (its
   `lk.agent.state`), or stayed silent for 1.5 s. The agent's words come from its own
   `lk.transcription` stream when it publishes one (`transcript_source: agent`), else tamesu
   transcribes the utterance with the STT model (`stt`); `auto` (the default) prefers the agent's.
   `result.json` records which (`transcript_source` events).
4. With `behaviors.interrupt_probability`, the caller starts its next turn 0.6 to 2 s into the
   agent's reply; `interruption_stop_ms` is how long the agent kept talking after the caller
   started, `talk_over_ratio` the overlap over the agent's total speech. `silence_after_turn_ms`
   adds a pause before each caller turn; `seed` makes both repeatable.
5. `audio.wav` in the run directory is the whole session: left channel the caller (noise
   included), right channel the agent, aligned on one clock.

The scripted example agent speaks and listens too (`--audio`), so `make integration` runs audio
mode end to end with the fake speech models.

## Keys

| Key | Meaning |
|---|---|
| `url` | The LiveKit server, `ws://` or `wss://`. |
| `api_key`, `api_secret` | As `{{secrets.NAME}}`; used to sign the join token only. |
| `room` | Room name template; default `tamesu-{{uuid}}`. |
| `dispatch.agent_name` | Requests an explicit agent dispatch when the room is created. |
| `agent_identity_prefix` | Identity prefix of the agent participant; default: a participant of kind agent, else any. |
| `mode` | `text` or `audio`. A test's `mode` and `--mode` override it. |
| `data_topic` | The text-input topic; default `lk.chat`. |
| `join_timeout` | How long to wait for the agent; default 30s. |
| `transcript_source` | Audio: `auto` (default), `agent` or `stt`. |

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | user text sent → agent turn complete |
| `time_to_first_token_ms` | user text sent → the agent's first text |
| `agent_join_ms` | room joined → the agent participant present |
| `turns` | the number of agent turns |
| `time_to_first_audio_ms` (`_p50`, `_p95`, `_max`) | audio: caller speech end → agent speech start (VAD) |
| `interruption_stop_ms` (`_p50`, `_p95`, `_max`) | audio: caller talks over → agent audio stops |
| `talk_over_ratio` | audio: overlapping speech / the agent's speech |
| `silence_gap_max_ms` | audio: the longest dead air while a reply was expected |
| `agent_speech_ms`, `user_speech_ms`, `interruptions` | audio: totals for the run |

`livekit.max_concurrent_rooms` in `tamesu.yaml` caps how many livekit tests run at once.

## Events

`result.json` records `joined`, `agent_joined`, `agent_left` and `disconnected` with
timestamps, plus in audio mode `audio_published`, `user_speech_start`/`_end`,
`agent_speech_start`/`_end`, `interruption`, `interruption_stopped`, `dtmf_sent` and
`transcript_source`: the beginning of the call timeline the sip target extends.

## Example

[`examples/livekit-agent`](https://github.com/xavidop/tamesu/tree/main/examples/livekit-agent) is
an agent built with the LiveKit Go SDK and Genkit that joins every `tamesu-*` room, in text mode
or (`--audio`) speaking and listening, and the suites that test it in both modes. `make integration` runs it against `livekit-server --dev` when the
binary or Docker is available:

```sh
livekit-server --dev &
(cd examples/livekit-agent && go run . --scripted) &
LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret tamesu run --config examples/livekit-agent/tamesu.llmfake.yaml
```

`tamesu doctor` probes every livekit target's server over HTTP.
