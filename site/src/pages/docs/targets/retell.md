---
layout: ../../../layouts/DocsLayout.astro
title: Retell target
---

# Retell target

![A laptop showing a browser window with a call button, a wave of colour flowing from it to a round robot with a headset, and a person with a clipboard watching from a doorway](/img/target-retell.jpg)

The `retell` target tests an agent hosted on **Retell** through a web call, the way Retell's own
browser SDK does: tamesu creates the call with Retell's API, joins the LiveKit room the call runs
in with the token Retell issues, and talks to the agent from inside the room. The conversation,
the recording, barge-in and the metrics are the [LiveKit target](/docs/targets/livekit/)'s in
audio mode; this target adds the call creation and Retell's room layout.

```yaml
targets:
  support:
    type: retell
    api_key: "{{secrets.RETELL_API_KEY}}"
    agent_id: agent_2b3c4d5e6f
    agent_version: latest_published                  # optional
    variables: { customer_name: Marta }              # retell_llm_dynamic_variables
    metadata: { suite: support }
    join_timeout: 20s
    idle: 2s
tests:
  - name: order status
    target: support
    persona: { name: Marta, style: "direct", language: en }
    scenario: { goal: "Ask where order ORD-1001 is", max_turns: 4 }
    assert:
      - judge: "The agent gives a delivery estimate or says how to get one"
      - metric: { name: time_to_first_audio_ms_p95, lt: 3000 }
```

The target needs the speech models (`speech.tts.model`, `speech.stt.model` in `tamesu.yaml`).

## How a call runs

1. `POST /v2/create-web-call` with the agent, its version, the dynamic variables and the
   metadata. The answer's `access_token` is a LiveKit token; its identity is read from the token.
2. tamesu joins the room on Retell's LiveKit cloud (`livekit_url`) with that token and waits
   `join_timeout` for Retell's `server` participant, which publishes the agent's audio.
3. From there the session is a LiveKit audio session: the caller's words are synthesised and
   published as a microphone track, the agent's audio is transcribed, turns end after `idle`,
   barge-in follows `behaviors.interrupt_probability`, and the call ends when the agent leaves
   the room.

Retell's newer `v3` endpoint issues tokens for its own WebRTC gateway, which tamesu cannot join;
the target uses `v2` and fails to start, with a clear message, if the answer names another
transport.

## Options

| Field | Meaning |
|---|---|
| `api_key` | The Retell API key, as `{{secrets.NAME}}`. |
| `agent_id`, `agent_version` | The agent to call, and optionally its version (`latest_published`, `v3`...). |
| `variables` | The `retell_llm_dynamic_variables` (templates allowed). |
| `metadata` | Stored on the call. |
| `livekit_url` | The LiveKit server of the web call, default Retell's cloud. |
| `join_timeout` | How long to wait for the agent to join, default `20s`. |
| `idle` | Silence after the agent's last audio that ends its turn, default `2s`. |
| `turn_timeout` | How long one turn may take, default `60s`. |
| `transcript_source` | `auto`, `agent` or `stt`, as for the LiveKit target. |
| `base_url` | The API, default `https://api.retellai.com`. |

## Metrics and events

The LiveKit audio metrics (`agent_join_ms`, `time_to_first_audio_ms`, `agent_speech_ms`,
`user_speech_ms`, `interruptions`, `interruption_stop_ms`, `turns`) and events, plus a
`retell_call` event with the call id, so the call can be found in Retell's dashboard.

## A phone call instead

To test a Retell agent on its phone number, dial it with the [SIP target](/docs/targets/sip/).
