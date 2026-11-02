---
layout: ../../../layouts/DocsLayout.astro
title: Realtime target
---

# Realtime target

The `realtime` target tests an agent that speaks the **OpenAI Realtime protocol** over a
websocket: the Realtime API itself, with your instructions and tools as the agent under test, or a
server that fronts your own agent with the same events (many voice stacks expose it). **Text
mode** sends each user turn as a conversation item and reads the response's text deltas: fast and
cheap. **Audio mode** speaks the turn as 24 kHz PCM16 through the configured TTS model, reads the
response's audio and its transcript, and leaves a stereo WAV of every run.

```yaml
targets:
  rt:
    type: realtime
    url: wss://api.openai.com/v1/realtime          # default; ws://, wss://, http(s):// upgraded
    model: gpt-realtime                           # added as ?model= when the url has none
    headers: { Authorization: "Bearer {{secrets.OPENAI_API_KEY}}" }
    session_update:                               # merged into the session.update event
      instructions: "You are the support agent of Mochi Shoes. ..."
      tools:
        - { type: function, name: lookup_policy, description: "...", parameters: { type: object, properties: { topic: { type: string } } } }
        - { type: function, name: end_session, description: "Call when the customer is done.", parameters: { type: object, properties: {} } }
      audio: { output: { voice: marin } }
    mode: text                                    # text | audio
    protocol: ga                                  # ga | beta (the older event names)
    done_tool: end_session                        # a call to this function ends the session
    turn_timeout: 60s                             # how long to wait for response.done
```

## How a turn works

1. tamesu connects with the rendered headers, sends `session.update` (the mode's defaults with
   your `session_update` merged on top) and waits briefly for `session.updated`; an `error`
   event here fails the test as an infrastructure error. A server that never confirms is given
   up on after 3 s, not failed.
2. **Text mode**: each user turn is a `conversation.item.create` (`input_text`) followed by
   `response.create`. **Audio mode**: the turn is synthesised with `speech.tts`, resampled to
   24 kHz and sent as 100 ms `input_audio_buffer.append` chunks, then `input_audio_buffer.commit`
   and `response.create`. Server-side turn detection is switched off, so the agent answers when
   asked and nothing else.
3. The reply is read until `response.done`: `response.output_text.delta` and
   `response.output_audio_transcript.delta` build the text (the beta names `response.text.delta`
   and `response.audio_transcript.delta` too), `response.output_audio.delta` is decoded into the
   agent's track, and function calls (`response.function_call_arguments.done`, or the
   `function_call` item) are recorded as tool calls, once per call id. A call to `done_tool`
   ends the session, which is what `scenario.stop_when` and `max_turns` otherwise do.
4. A `response.done` with status `failed`, an `error` event, a closed connection or no
   `response.done` within `turn_timeout` is a transport error for the turn.

Function calls are **recorded, not executed**: tamesu never answers them with
`function_call_output`, so when you test the Realtime API directly the model continues without
tool results. Point the target at your own server when the tools matter.

## Keys

| Key | Meaning |
|---|---|
| `url` | The websocket endpoint (default `wss://api.openai.com/v1/realtime`). Templates apply. |
| `model` | Added as `?model=` when the URL has none, such as `gpt-realtime`. |
| `headers` | Handshake headers. The OpenAI API needs `Authorization: Bearer {{secrets.OPENAI_API_KEY}}`; `validate` warns when it is missing. With `protocol: beta`, `OpenAI-Beta: realtime=v1` is added for you. |
| `session_update` | Merged (recursively for maps) over the defaults into `session.update`: text mode sends `{type: realtime, output_modalities: [text]}`, audio mode `{type: realtime, output_modalities: [audio], audio: {input: {format: audio/pcm 24000, turn_detection: null}, output: {format: audio/pcm 24000}}}` (beta: `modalities`, `input_audio_format: pcm16`). |
| `mode` | `text` (default) or `audio`; a test's `mode` or `--mode` overrides it. Audio mode needs `speech.tts` in `tamesu.yaml`. |
| `protocol` | `ga` (default) or `beta`, the event dialect of the server. |
| `done_tool` | The function whose call ends the session (default `end_session`). Give the agent such a tool, or rely on `stop_when` and `max_turns`. |
| `turn_timeout` | The wait for `response.done`, and the handshake (default `60s`). |

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | the turn was sent → `response.done` |
| `time_to_first_token_ms` | the turn was sent → the first text, transcript or audio delta |
| `time_to_first_audio_ms` (`_p50`, `_p95`, `_max`) | audio mode: `input_audio_buffer.commit` → the first audio delta |
| `agent_speech_ms`, `user_speech_ms` | audio mode: how much each side spoke |
| `agent_input_tokens`, `agent_output_tokens` | the agent's own token usage from `response.done`, summed over the session |
| `turns` | the number of agent turns |
| `ws_messages_received` | the server events of the session |

Timestamps are taken around the socket, never from a model. Audio mode has no voice activity
detection, interruptions or line simulation, since the protocol itself marks where a turn starts
and ends; for those, test through the [LiveKit](/docs/targets/livekit/) or [SIP](/docs/targets/sip/)
target.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except `status`, `json_path` and the
phone ones; `tool_called` sees the agent's function calls:

```yaml
assert:
  - judge: "The agent states the 30-day return policy and does not promise an exception"
  - tool_called: { name: lookup_policy, args: { topic: returns } }
  - tool_not_called: issue_refund
  - metric: { name: time_to_first_audio_ms_p95, lt: 1500 }
  - turns: { lte: 6 }
```

## Artifacts

Text mode leaves the transcript and `result.json` like any conversational test, with every
server event of each turn in the turn's raw payload. Audio mode adds `audio.wav`: a 24 kHz stereo
file with the simulated user on the left and the agent on the right, which the HTML report plays.
