---
layout: ../../../layouts/DocsLayout.astro
title: Pipecat target
---

# Pipecat target

The `pipecat` target tests a [Pipecat](https://pipecat.ai) agent through its **websocket
transport** with the protobuf frame serializer, the transport most Pipecat bots expose for
browser and native clients. tamesu connects as the client: in **text mode** it types each user
turn and reads the bot's text; in **audio mode** it speaks the turn as audio frames and reads the
bot's voice, its transcript and the RTVI events around them.

```yaml
targets:
  bot:
    type: pipecat
    url: wss://bot.example.com/ws                 # the websocket of the transport
    headers: { Authorization: "Bearer {{secrets.BOT_TOKEN}}" }
    mode: text                                    # text | audio
    sample_rate: 16000                            # audio mode: the transport's rate
    idle: 2s                                      # end of turn when the bot stops without saying so
    turn_timeout: 60s
    tool_results:                                 # answers to client-side function calls
      lookup_policy: { topic: "{{args.topic}}", window_days: 30 }
```

The bot needs an RTVI observer in its pipeline (`RTVIObserver`), which Pipecat's starter
templates include: it is what tells tamesu when the bot starts and stops talking and carries the
bot's words.

## How a turn works

1. tamesu connects, sends `client-ready` and waits briefly for `bot-ready`; a transport that never
   sends it is given up on after 3 s, not failed.
2. **Text mode**: the user turn is a `send-text` message (`run_immediately: true`,
   `audio_response: false`). The bot's `bot-llm-text` deltas make the turn's text (else
   `bot-tts-text` or `bot-transcription`), and `bot-llm-stopped` ends it. **Audio mode**: the turn
   is synthesised with `speech.tts`, streamed as 20 ms frames at `sample_rate` in real time with
   half a second of silence after it, so the bot's voice activity detector hears the end. The
   bot's audio frames go on the right track of `audio.wav`, its `bot-tts-text` or
   `bot-transcription` is the turn's text (its audio is transcribed with `speech.stt` when it
   sends neither), and `bot-stopped-speaking` ends the turn.
3. Without a stop message the turn ends after `idle` with no further sign of the bot;
   `turn_timeout` bounds the whole turn.
4. Function calls are recorded as tool calls from `llm-function-call-in-progress` (functions the
   bot runs itself) and `llm-function-call` (functions it asks the client to run). A client-side
   function listed under `tool_results` is answered with `llm-function-call-result` and the turn
   goes on with the bot's reaction; up to five answers per turn.

A bot that says goodbye does not end the session on this transport, so rely on
`scenario.stop_when` and `max_turns`.

## Keys

| Key | Meaning |
|---|---|
| `url` | The transport's websocket endpoint. Templates apply. |
| `headers` | Handshake headers; credentials as `{{secrets.NAME}}`. |
| `mode` | `text` (default) or `audio`; a test's `mode` or `--mode` overrides it. Audio mode needs `speech.tts` (and `speech.stt` for bots that send no text) in `tamesu.yaml`. |
| `sample_rate` | Audio mode: the rate of the frames sent and of `audio.wav` (default 16000). The bot's frames are resampled to it when they differ. |
| `idle` | Silence after the bot's last message or audio that ends a turn when no stop message arrives (default `2s`). |
| `turn_timeout` | The wait for the end of a turn, and the connection (default `60s`). |
| `tool_results` | Answers to client-side function calls, by name: any YAML value, string leaves may use `{{args.<key>}}`. |

## Metrics

| Metric | Definition |
|---|---|
| `turn_latency_ms` (`_p50`, `_p95`, `_max`) | the turn was sent → the bot stopped |
| `time_to_first_token_ms` | the turn was sent → the first text or audio from the bot |
| `time_to_first_audio_ms` (`_p50`, `_p95`, `_max`) | audio mode: the caller's last frame → the bot's first |
| `agent_speech_ms`, `user_speech_ms` | audio mode: how much each side spoke |
| `turns` | the number of bot turns |
| `ws_messages_received` | the frames and messages of the session |

Timestamps are taken around the socket, never from a model.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except `status`, `json_path` and the
phone ones; `tool_called` sees the bot's function calls.

## Artifacts

Text mode leaves the transcript and `result.json` like any conversational test, with every RTVI
message of each turn in the turn's raw payload. Audio mode adds `audio.wav`, a stereo file at
`sample_rate` with the simulated user on the left and the bot on the right, which the HTML report
plays.
