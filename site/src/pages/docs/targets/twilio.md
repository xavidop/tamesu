---
layout: ../../../layouts/DocsLayout.astro
title: Twilio target
---

# Twilio target

![A phone sending a sound wave into a tunnel of rings with a robot at the far end](/img/target-twilio.jpg)

The `twilio` target tests a voice agent built on **Twilio Media Streams** without a phone number,
a trunk or a call: tamesu plays Twilio's side of the websocket. It connects to the agent's stream
URL (the `<Stream url="…">` of its TwiML), sends `connected` and `start` with the media format
and your custom parameters, streams the simulated caller's speech as μ-law `media` events at
8 kHz, sends `dtmf` events, acknowledges the agent's `mark`s once their audio has played, honours
`clear`, and reads the agent's `media` as its speech. Agents built on Twilio directly, on Vapi-
or Retell-style frameworks that expose a Media Streams endpoint, or on any server that speaks the
protocol are tested the same way.

```yaml
targets:
  phone:
    type: twilio
    url: "${AGENT_STREAM_URL:-wss://agent.example.com/media}"
    headers: { Authorization: "Bearer {{secrets.STREAM_TOKEN}}" }
    parameters: { caller_tier: gold, language: en }     # customParameters of the start event
    from: "+15550100"                                   # From and To the agent reads from customParameters
    to: "+15550200"
tests:
  - name: return after 40 days
    target: phone
    persona: { name: Marta, style: "polite, slightly confused", language: en, voice: en-US-female }
    scenario:
      goal: "Find out if you can return shoes bought 40 days ago"
      max_turns: 6
      script:
        - wait_for: "how can I help"
        - dtmf: "2"
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - metric: { name: time_to_first_audio_ms, p95: { lt: 2500 } }
```

The target is audio only, like the phone: it needs the speech models (`speech.tts.model` and
`speech.stt.model` in `tamesu.yaml`) to speak the persona's turns and to hear the agent, and
the persona's `voice`, `language` and behaviours apply as on the [LiveKit](/docs/targets/livekit/)
and [SIP](/docs/targets/sip/) targets. Audio travels as it would on a call: 8 kHz μ-law, 20 ms
chunks, paced in real time, with a short silence after each utterance so the agent's voice
activity detector sees the end of the turn.

## Keys

| Key | Meaning |
|---|---|
| `url` | The agent's Media Streams websocket. `ws://`, `wss://`, or `http(s)://` for a plain upgrade. |
| `headers` | Handshake headers; credentials as `{{secrets.NAME}}`. |
| `parameters` | The `customParameters` of the `start` event, what `<Parameter name="…" value="…"/>` sets in TwiML. Values render `{{vars.x}}` and `{{secrets.X}}`. |
| `from`, `to` | Added to `customParameters` as `From` and `To`, where agents usually read the caller and the dialled number. |
| `idle` | Silence after the agent's last media that ends its turn (default `2s`). |
| `turn_timeout` | How long to wait for the agent to answer a turn (default `60s`). |

## What a run records

Each turn is the agent's audio transcribed by the STT model (as it arrives, when the model
streams: Deepgram, `assemblyai/universal`, `soniox/stt-rt-v3`; the `stt_ms` metric is that wait), with the usual turn metrics;
`time_to_first_audio_ms` is measured from the end of the caller's utterance to the agent's first
media event, `greeting_ms` from `start` to the agent's first media when it speaks first,
`call_duration_ms` from `start` to `stop`, plus `agent_speech_ms`, `user_speech_ms`, `marks`
and `clears`. The stereo `audio.wav` (left the caller, right the agent) is in the run directory
and plays in the HTML report. The events list `start`, `greeting`, `dtmf`, every `mark` the
agent asked for and every `clear` it sent, so a barge-in handler's behaviour is visible.

Marks are answered the way Twilio does it: when the agent sends `mark`, tamesu replies with the
same mark once the audio queued before it has played out in real time, so an agent that waits
for a mark before listening again works unchanged. `clear` empties the playback queue, which
is what the agent uses when the caller interrupts.

## Without the phone

This target removes Twilio, the carrier and the number from the loop, so it runs in CI against
the agent's websocket alone and costs nothing but the speech models. What it cannot test is the
part it removes: real call setup, the carrier's audio, DTMF detection on a real line. For that,
the [SIP target](/docs/targets/sip/) places real calls through a LiveKit SIP service.

## Barge-in

A caller does not wait for the agent to finish. With `behaviors.interrupt_probability` on the
persona, tamesu stops listening partway through an agent turn (after 0.6 to 2 seconds of its
audio, at random, repeatable with the test's `seed`) and speaks its next line while the agent's
audio is still playing. Twilio's protocol has one right answer to that: the agent detects the
caller's speech and sends `clear` to drop the audio it had queued. `interruption_stop_ms` is the
time from the caller's first word to that `clear`; when the agent never clears, it is the time
until the audio it had already queued would have played out, which is what the caller heard.
`interruptions` counts them; the turn the caller cut short is marked `interrupted` in the
transcript, and the events read `interrupting`, `interruption`, `clear` and `interruption_stopped`
with the measured time. An `interrupt_missed` event means the agent had finished before the
caller got to speak.

```yaml
persona:
  name: Marta
  behaviors: { interrupt_probability: 0.5 }
assert:
  - metric: { name: interruption_stop_ms_p95, lt: 800 }
```
