---
layout: ../../../layouts/DocsLayout.astro
title: LiveKit target
---

# LiveKit target

The `livekit` target tests a realtime voice agent by joining its LiveKit room as a simulated
participant, whatever the agent was built with: LiveKit Agents, Voiceflow, Pipecat, a custom
participant. **Text mode** (this page) exchanges text streams, so a text-capable agent is tested
without audio: fast, cheap, deterministic. Audio mode (TTS in, VAD and STT out, interruptions,
noise) is planned with the audio pipeline.

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

## Keys

| Key | Meaning |
|---|---|
| `url` | The LiveKit server, `ws://` or `wss://`. |
| `api_key`, `api_secret` | As `{{secrets.NAME}}`; used to sign the join token only. |
| `room` | Room name template; default `tamesu-{{uuid}}`. |
| `dispatch.agent_name` | Requests an explicit agent dispatch when the room is created. |
| `agent_identity_prefix` | Identity prefix of the agent participant; default: a participant of kind agent, else any. |
| `mode` | `text` (this page) or `audio` (planned). A test's `mode` and `--mode` override it. |
| `data_topic` | The text-input topic; default `lk.chat`. |
| `join_timeout` | How long to wait for the agent; default 30s. |

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | user text sent → agent turn complete |
| `time_to_first_token_ms` | user text sent → the agent's first text |
| `agent_join_ms` | room joined → the agent participant present |
| `turns` | the number of agent turns |

The audio metrics (`time_to_first_audio_ms`, `interruption_stop_ms`, `talk_over_ratio`) come
with audio mode. `livekit.max_concurrent_rooms` in `tamesu.yaml` caps how many livekit tests
run at once.

## Events

`result.json` records `joined`, `agent_joined`, `agent_left` and `disconnected` with
timestamps, the beginning of the call timeline the sip target extends.

## Example

[`examples/livekit-agent`](https://github.com/xavidop/tamesu/tree/main/examples/livekit-agent) is
a text-mode agent built with the LiveKit Go SDK and Genkit that joins every `tamesu-*` room, and
the suite that tests it. `make integration` runs it against `livekit-server --dev` when the
binary or Docker is available:

```sh
livekit-server --dev &
(cd examples/livekit-agent && go run . --scripted) &
LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret tamesu run --config examples/livekit-agent/tamesu.llmfake.yaml
```

`tamesu doctor` probes every livekit target's server over HTTP.
