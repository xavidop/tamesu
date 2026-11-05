---
layout: ../../../layouts/DocsLayout.astro
title: "Recipes: Voice agents"
---

# Voice agents

The OpenAI Realtime protocol, a phone IVR over SIP, a Voiceflow voice agent through its SIP connection, and Vapi, Deepgram and Retell agents on the generic targets.

Each recipe is a whole project you can copy, change the URLs and run; see [Recipes](/docs/recipes/) for the other pages.

## A voice agent on the OpenAI Realtime protocol

```yaml
# tamesu.yaml
version: 1
suites: ["realtime.yaml"]
genkit:
  plugins: [googlegenai]
models: { default: googleai/gemini-2.5-flash }
speech:
  tts: { model: googleai/gemini-2.5-flash-preview-tts, voice: en-US-female }
  stt: { model: googleai/gemini-2.5-flash, language: en }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  OPENAI_API_KEY: env:OPENAI_API_KEY
```

```yaml
suite: realtime
targets:
  rt:
    type: realtime
    model: gpt-realtime
    headers: { Authorization: "Bearer {{secrets.OPENAI_API_KEY}}" }
    session_update:
      instructions: "You are the support agent of Mochi Shoes. Call lookup_policy before answering a policy question; call end_session when the customer is done."
      tools:
        - { type: function, name: lookup_policy, description: "Look up a store policy", parameters: { type: object, properties: { topic: { type: string } }, required: [topic] } }
        - { type: function, name: end_session, description: "The customer is done.", parameters: { type: object, properties: {} } }
    tool_results:
      lookup_policy: { topic: "{{args.topic}}", window_days: 30, exceptions: none }
    mode: audio
    audio_format: g711_ulaw
tests:
  - name: interrupted refund answer
    target: rt
    persona: { name: Marta, style: impatient, language: en, voice: en-US-female, behaviors: { interrupt_probability: 0.5 } }
    scenario: { goal: "Ask about returning shoes bought 40 days ago, then say you are done", max_turns: 4 }
    assert:
      - judge: "The agent states the 30-day return policy"
      - tool_called: lookup_policy
      - metric: { name: time_to_first_audio_ms_p95, lt: 2500 }
```

## A phone IVR through the SIP target

```yaml
# tamesu.yaml
version: 1
suites: ["ivr.yaml"]
genkit:
  plugins: [googlegenai]
models: { default: googleai/gemini-2.5-flash }
speech:
  tts: { model: googleai/gemini-2.5-flash-preview-tts }
  stt: { model: googleai/gemini-2.5-flash, language: en }
  vad: { provider: silero }
sip:
  livekit_url: "${LIVEKIT_URL:-ws://localhost:7880}"
  outbound_trunk_id: "${TAMESU_SIP_TRUNK:-ST_xxxx}"
  allowed_numbers: ["+1555000*"]
  max_concurrent_calls: 1
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  LIVEKIT_API_KEY: env:LIVEKIT_API_KEY
  LIVEKIT_API_SECRET: env:LIVEKIT_API_SECRET
```

```yaml
suite: ivr
targets:
  ivr: { type: sip, direction: outbound, to: "+15550001", from: "+15550002", ring_timeout: 20s, max_call_duration: 2m }
tests:
  - name: the IVR routes 2 to billing
    target: ivr
    seed: 1
    persona: { name: Marta, style: calm, language: en, voice: en-US-female, behaviors: { background_noise: street, line_quality: pstn } }
    scenario:
      goal: "Reach the billing department and ask about a double charge"
      max_turns: 3
      script:
        - wait_for: "press 2 for billing"
        - dtmf: "2"
        - wait_for: "billing department"
    assert:
      - dtmf_received: { equals: "9" }
      - call_ended_by: [agent, caller]
      - metric: { name: call_setup_ms, lt: 15000 }
```

## A Vapi assistant over its websocket transport

Vapi exposes an assistant on a websocket when the call is created with the `vapi.websocket`
transport: `POST /call` answers a `websocketCallUrl`, the socket carries raw PCM both ways, and
Vapi's control messages (transcripts, speech updates, tool calls, the end of the call) travel as
JSON on the same socket. The `voice` target does all of it from YAML: `session.open` creates
the call and captures the URL, and `messages` names the control messages.

```yaml
suite: vapi-assistant
tags: [voice, vapi]
vars: { assistant_id: "${VAPI_ASSISTANT_ID:-asst_000}" }
secrets: { VAPI_API_KEY: env:VAPI_API_KEY }
targets:
  assistant:
    type: voice
    session:
      open:
        url: https://api.vapi.ai/call
        headers: { Authorization: "Bearer {{secrets.VAPI_API_KEY}}" }
        body:
          assistantId: "{{vars.assistant_id}}"
          assistantOverrides: { variableValues: { customer_name: Marta } }
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
      - judge: "The assistant states the 30-day window and does not promise an exception"
      - tool_called: lookup_policy
      - metric: { name: time_to_first_audio_ms_p95, lt: 2500 }
  - name: it stops when interrupted
    target: assistant
    persona: { name: Ken, style: "impatient", language: en, behaviors: { interrupt_probability: 1 } }
    scenario: { goal: "Ask about shipping times and cut the assistant off", max_turns: 3 }
    assert:
      - metric: { name: interruption_stop_ms_p95, lt: 800 }
```

A `mulaw` 8 kHz call is the same suite with `audioFormat: { format: mulaw, container: raw,
sampleRate: 8000 }` and `audio: { encoding: mulaw }`. The same Vapi assistant on a phone number
is dialled with the [SIP target](/docs/targets/sip/).

## A Deepgram Voice Agent

Deepgram's agent speaks binary audio both ways on one websocket, configured by a first
`Settings` message, and reports what it heard and said as `ConversationText` messages. The
`voice` target sends the settings as `messages.open` and reads the agent's lines from the
transcript messages.

```yaml
suite: deepgram-agent
tags: [voice, deepgram]
secrets: { DEEPGRAM_API_KEY: env:DEEPGRAM_API_KEY }
targets:
  agent:
    type: voice
    url: wss://agent.deepgram.com/v1/agent/converse
    headers: { Authorization: "Token {{secrets.DEEPGRAM_API_KEY}}" }
    audio: { encoding: pcm16, sample_rate: 16000, frames: binary }
    messages:
      open:
        type: Settings
        audio:
          input: { encoding: linear16, sample_rate: 16000 }
          output: { encoding: linear16, sample_rate: 16000, container: none }
        agent:
          listen: { provider: { type: deepgram, model: nova-3 } }
          think: { provider: { type: open_ai, model: gpt-4o-mini }, prompt: "You are the Mochi Shoes support agent. Returns are accepted within 30 days of delivery, no exceptions." }
          speak: { provider: { type: deepgram, model: aura-2-thalia-en } }
          greeting: "Hi, this is Mochi Shoes. How can I help?"
      transcript: "$[?(@.type=='ConversationText' && @.role=='assistant')].content"
      interrupted: "$[?(@.type=='UserStartedSpeaking')].type"
      tool_calls: "$[?(@.type=='FunctionCallRequest')].functions[*]"
    idle: 2s
tests:
  - name: return after 40 days
    target: agent
    persona: { name: Marta, style: "polite", language: en, voice: en-US-female }
    scenario: { goal: "Find out if the shoes you bought 40 days ago can still be returned", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - metric: { name: greeting_ms, lt: 4000 }
```

The prompt in `Settings` is the agent under test; keep it in a file and `{{vars}}` it in when
the suite grows. A protocol that wraps audio in JSON instead of binary frames sets
`audio: { frames: json, send: { ... "{{audio}}" }, receive: "$[*].path.to.base64" }` and is
otherwise the same.

## A Voiceflow voice agent, over SIP

Voiceflow's runtime takes text over its WebSocket and Conversations API; its voice agents hear
through telephony only: a Twilio, Vonage, Telnyx or Voiceflow number, or a **Voiceflow SIP**
connection, an address of the form `<uuid>.sip.voiceflow.com` that answers `sip:<number>@<address>`
on UDP or TCP 5060 (TLS on 5061), without `REGISTER`, authenticated by your IP, by credentials or
both. So the voice agent is tested with the [SIP target](/docs/targets/sip/): the trunk points at
the Voiceflow SIP address, the caller ID goes in `From` as Voiceflow requires, and tamesu dials.
Any number works as `to`: the connection is bound to one agent. Call forwarding in the agent is
a SIP `REFER`, which `transferred_to` asserts on; a Capture step collecting digits is answered
with `dtmf` script steps.

```yaml
# tamesu.yaml
version: 1
suites: ["voiceflow-voice.yaml"]
genkit: { plugins: [googlegenai] }
models: { default: googleai/gemini-2.5-flash }
speech:
  tts: { model: googleai/gemini-2.5-flash-preview-tts }
  stt: { model: googleai/gemini-2.5-flash, language: en }
  vad: { provider: silero }
sip:
  livekit_url: "${LIVEKIT_URL:-ws://localhost:7880}"
  outbound_trunk_id: "${TAMESU_SIP_TRUNK:-ST_xxxx}"      # created by tamesu sip setup from the trunk below
  allowed_numbers: ["+1555*"]
  max_concurrent_calls: 2                                 # SIP calls count against the agent's phone concurrency
  trunk:
    address: 3f2c9a1e-5b7d-4e8a-9c61-2d4b8f0e7a13.sip.voiceflow.com   # the connection's SIP address, never an IP
    numbers: ["+15550100"]                                # the caller ID Voiceflow reads from From
    auth_username: "{{secrets.VF_SIP_USER}}"              # the connection's Credentials; omit with IP authentication
    auth_password: "{{secrets.VF_SIP_PASSWORD}}"
    transport: tcp
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  LIVEKIT_API_KEY: env:LIVEKIT_API_KEY
  LIVEKIT_API_SECRET: env:LIVEKIT_API_SECRET
  VF_SIP_USER: env:VF_SIP_USER
  VF_SIP_PASSWORD: env:VF_SIP_PASSWORD
```

```yaml
suite: voiceflow-voice
tags: [voice, voiceflow]
targets:
  agent:
    type: sip
    direction: outbound
    to: "+15550199"                             # any E.164 number: the SIP connection is assigned to one agent
    from: "+15550100"
    ring_timeout: 20s
    max_call_duration: 3m
tests:
  - name: returns policy, spoken
    target: agent
    persona: { name: Marta, style: "polite, a little hard of hearing", language: en, voice: en-US-female }
    scenario: { goal: "Ask whether shoes bought 40 days ago can still be returned", max_turns: 5 }
    assert:
      - judge: "The agent states the 30-day return window and offers the voucher instead of an exception"
      - call_ended_by: [agent, caller]
      - metric: { name: call_setup_ms, lt: 15000 }
      - metric: { name: time_to_first_audio_ms_p95, lt: 3000 }
  - name: the order number is collected by keypad
    target: agent
    persona: { name: Marta, style: direct, language: en }
    scenario:
      goal: "Find out where order 1001 is"
      max_turns: 4
      script:
        - wait_for: "enter your order number"
        - dtmf: "1001#"
    assert:
      - contains: "1001"
      - judge: "The agent gives a delivery status for the order"
  - name: a billing question is forwarded
    target: agent
    persona: { name: Marta, style: insistent, language: en }
    scenario: { goal: "Dispute a double charge and insist on speaking to billing", max_turns: 4 }
    assert:
      - transferred_to: { equals: "+15550155" }
      - pack: discloses_ai
```

The same suite tests the agent behind a Twilio, Vonage or Telnyx number instead: keep your own
trunk in `sip.trunk` and put the agent's phone number in `to`. The runtime's `audioEvents`
setting makes the WebSocket return the agent's TTS as `audio` traces, which is useful for
listening to a reply but is not a way to speak to the agent; the text recipe is under
[Messaging channels](/docs/recipes/channels/#a-voiceflow-agent-over-its-conversations-api).

## A Retell agent through a web call

Retell's web calls run in a LiveKit room: `create-web-call` answers an access token for it.
`session.open` creates the call and the LiveKit target joins Retell's cloud with that token,
as the browser SDK does; Retell's `server` participant is the agent.

```yaml
suite: retell-agent
tags: [voice, retell]
vars: { agent_id: "${RETELL_AGENT_ID:-agent_000}" }
secrets: { RETELL_API_KEY: env:RETELL_API_KEY }
targets:
  agent:
    type: livekit
    url: wss://retell-ai-4ihahnq7.livekit.cloud          # the LiveKit cloud Retell's web calls run on
    session:
      open:
        url: https://api.retellai.com/v2/create-web-call
        headers: { Authorization: "Bearer {{secrets.RETELL_API_KEY}}" }
        body: { agent_id: "{{vars.agent_id}}", retell_llm_dynamic_variables: { customer_name: Marta } }
        capture: { token: $.access_token, call_id: $.call_id }
    token: "{{session.token}}"
    room: "{{session.call_id}}"
    agent_identity_prefix: server
    mode: audio
    join_timeout: 20s
tests:
  - name: order status
    target: agent
    persona: { name: Marta, style: direct, language: en, voice: en-US-female }
    scenario: { goal: "Ask where order ORD-1001 is", max_turns: 4 }
    assert:
      - judge: "The agent gives a delivery estimate or says how to get one"
      - metric: { name: time_to_first_audio_ms_p95, lt: 3000 }
```

Retell's newer `v3` endpoint issues tokens for its own WebRTC gateway rather than LiveKit; the
`v2` endpoint above is the one that works with the LiveKit target. The same agent on a phone
number is dialled with the [SIP target](/docs/targets/sip/).
