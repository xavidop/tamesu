---
layout: ../../../layouts/DocsLayout.astro
title: SIP target
---

# SIP target

The `sip` target tests a phone agent by calling it (or being called by it) through a LiveKit
SIP service and a trunk you bring: Twilio, Telnyx, Plivo, your own PBX. The call is bridged
into a LiveKit room and tamesu talks to it from inside that room exactly like the
[LiveKit target](livekit) in audio mode: TTS in, VAD and STT out, interruptions, pauses,
background noise, a stereo WAV per run. The agent never knows it is being tested, whatever it
was built with (Voiceflow, Vapi, Retell, a Twilio IVR, Agentforce, a human queue). This page
adds what is specific to a phone call; [SIP setup](../sip-setup) covers the service and trunks.

```yaml
targets:
  phone:
    type: sip
    direction: outbound                           # outbound: tamesu calls the agent | inbound: the agent calls tamesu
    to: "+18005550100"                            # the agent's number (outbound)
    from: "+34910000000"                          # caller ID presented by the trunk
    trunk_id: ST_xxxxxxxx                         # defaults to sip.outbound_trunk_id
    ring_timeout: 30s
    max_call_duration: 5m
    # inbound only: how to make the agent call us on sip.inbound_number
    # trigger: { http: { url: https://api.example.com/campaigns/test-call, method: POST,
    #            body: { to: "{{sip.inbound_number}}" }, headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" } } }
    # trigger: { command: "./scripts/dial.sh $TAMESU_INBOUND_NUMBER" }
```

The project's `sip:` block ([configuration](../configuration)) holds `livekit_url`,
`outbound_trunk_id`, `inbound_number`, `inbound_room`, `allowed_numbers`,
`max_concurrent_calls` and the `trunk` for `tamesu sip setup`. The LiveKit keys come from the
secrets `LIVEKIT_API_KEY` and `LIVEKIT_API_SECRET`.

## Outbound calls

1. tamesu joins a fresh room `tamesu-sip-<uuid>` and asks the SIP service to dial `to`
   through the trunk with `from` as caller ID (`CreateSIPParticipant`, waiting until answered,
   with `ring_timeout` and `max_call_duration`).
2. The SIP participant's `sip.callStatus` attribute drives the timeline: `dialing`, `ringing`,
   `call_active`, and the metrics `call_setup_ms` (dial → active) and `ring_ms` (ringing →
   active). `greeting_ms` is active → the agent's first speech, `call_duration_ms` active →
   hangup.
3. The conversation runs as in audio mode. The caller's DTMF digits (a `dtmf` from the
   simulator or a `dtmf:` script step) go out as RFC 4733 events and as tones; digits the agent
   sends arrive as `dtmf_received` events.
4. A call that does not connect is a result, not an error: `call_ended_by` is `busy` (486,
   603), `no_answer` (480, 408, nobody within `ring_timeout`) or `error`, the first turn
   reports the agent gone, and the assertions run against that.
5. When the test ends tamesu hangs up (`call_ended_by: caller`); when the far end hangs up
   first it is `agent`, or `timeout` past `max_call_duration`. A transfer reported by the PBX
   (a `tamesu.transfer` data packet, or a SIP participant attribute mentioning a REFER target)
   is a `transfer` event for `transferred_to`.

## Inbound calls

For outbound campaigns and callbacks: tamesu joins `sip.inbound_room` (the dispatch rule
`tamesu sip setup` created routes calls to `inbound_number` into it), fires the `trigger` (an
HTTP request or a shell command, both with `{{sip.inbound_number}}` and secrets rendered; the
command also gets `TAMESU_INBOUND_NUMBER` and `TAMESU_ROOM`), waits up to `ring_timeout` for
the SIP participant, then runs the conversation. Nothing arriving is `call_ended_by:
no_answer`. Inbound tests take turns on the room, so they run one at a time.

## Scripts for IVRs

```yaml
scenario:
  goal: "Reach the billing department and ask about a double charge"
  script:
    - wait_for: "press 2 for billing"            # fuzzy-matched on the transcript, 30 s timeout
    - dtmf: "2"
    - wait_for: "billing"
    - say: "Hi, I was charged twice for order 1234"
```

`wait_for` listens (empty turns) until the agent's words fuzzily contain the text (one word in
five may differ, digits match their spelled form), `dtmf` sends digits, `say` speaks exact text;
then the persona takes over with the scripted exchange in its history. A step that is never
heard fails the test with a `script` assertion naming it.

## Safety

- `sip.allowed_numbers` are E.164 patterns (`+1800555*`). A target whose `to` is a literal number
  is validated against them when the list is set; a `to` that comes from a variable or secret
  requires the list, and `tamesu run` refuses the suite (exit 2) otherwise. The target checks
  the rendered number again before dialing.
- `sip.max_concurrent_calls` (default 2) caps parallel calls; every call has
  `max_call_duration` (default 5m) enforced by the SIP service.

## Metrics and assertions

| Metric | Definition |
|---|---|
| `call_setup_ms` | dial (or trigger) → call active |
| `ring_ms` | ringing → answered |
| `greeting_ms` | call active → the agent's first speech |
| `call_duration_ms` | active → hangup |
| the audio metrics | `time_to_first_audio_ms`, `interruption_stop_ms`, `talk_over_ratio`, `silence_gap_max_ms`, `turn_latency_ms`, as in [LiveKit audio mode](livekit) |

`dtmf_received: { equals: "1234#" }`, `call_ended_by: [agent, caller]` and
`transferred_to: { matches: "^sip:billing@" }` read the call events; `result.json` keeps the
whole timeline (`dialing`, `ringing`, `call_active`, `dtmf_sent`, `dtmf_received`, `transfer`,
`call_ended`, `call_end_detail`) with timestamps.

## Example

[`examples/sip-ivr`](https://github.com/xavidop/tamesu/tree/main/examples/sip-ivr) calls an
Asterisk IVR through a LiveKit SIP service, all in Docker (`examples/compose.yaml`:
livekit-server, Redis, `livekit/sip`, Asterisk as carrier and IVR), navigates the menu with
`wait_for`/`dtmf` and checks the digits and the hangup. `make integration` runs it when Docker
is available; no phone number or trunk account is involved. A run against a real trunk is the
same suite with your `sip:` block.
