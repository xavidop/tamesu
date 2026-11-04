---
layout: ../../layouts/DocsLayout.astro
title: Personas and scenarios
---

# Personas and scenarios

![Four different people figures with speech bubbles, one holding a telephone](/img/personas.jpg)

A conversational test (rest, grpc, livekit, sip, realtime, pipecat) is driven by a simulated user: a **persona** (who is
talking) with a **scenario** (what they want and when to stop). The simulator reads the persona,
the scenario and the conversation so far and produces the next user turn, or decides to stop; in
traces it is the `simulateUserTurn` flow.

```yaml
tests:
  - name: reschedule booking by voice
    target: voice
    persona:
      name: Alice
      style: impatient
      language: en-GB
      voice: en-GB-female
      behaviors:
        interrupt_probability: 0.3
        silence_after_turn_ms: [300, 1500]
        background_noise: cafe
    scenario:
      goal: "You booked a room for Friday; move it to Saturday"
      max_turns: 12
      stop_when: "the agent confirmed the new date"
    seed: 42
```

## Persona

| Key | Meaning |
|---|---|
| `name` | How the persona introduces itself when asked. |
| `style` | How it talks: `polite, slightly confused`, `impatient`, `terse`, `chatty`. This shapes every turn. |
| `language` | The language it speaks (`es`, `en-GB`); the persona answers only in it, so a bilingual agent is tested one language at a time. |
| `voice` | The TTS voice for audio mode (`en-GB-female`); ignored in text mode. |
| `behaviors.interrupt_probability` | Audio: the chance per turn that the persona starts talking before the agent finishes (measures `interruption_stop_ms`). |
| `behaviors.silence_after_turn_ms` | Audio: `[min, max]` milliseconds of silence after the agent's turn before answering. |
| `behaviors.background_noise` | Audio: a noise preset (`cafe`, `street`, `office`, `white`) or a `.wav` file mixed under the caller's voice, at `behaviors.background_noise_snr_db` (default 20 dB). |
| `behaviors.line_quality` | Audio and sip: `pstn` (8 kHz μ-law), `mobile_poor` (A-law with loss and jitter) or `voip_good` applied to the caller's audio. |
| `adversarial` | Red team: the persona is an attacker that pursues the goal with a different tactic every turn and records each one; see [the adversarial persona](/docs/red-team/#the-adversarial-persona). `tactics` replaces the built-in list. |

Give the persona what the agent will ask for: a name, an order reference in the goal, a date. The
simulator invents plausible details that fit the persona when it must, and never gives a real
secret, card number or password. The audio behaviours take effect in livekit audio mode and on
sip calls; `interrupt_probability` also in realtime, pipecat and twilio audio mode (the barge-in
test: the caller talks over the agent and `interruption_stop_ms` measures how long it kept
talking). Text mode ignores them. A test's `seed` drives the behaviour RNG, so a run
with the same seed pauses and interrupts at the same points.

## Scenario

| Key | Meaning |
|---|---|
| `goal` | What the persona is trying to achieve, in the second person: "Find out if you can return shoes bought 40 days ago". Optional when the scenario is only a `script`: the steps play and the conversation stops. |
| `max_turns` | The turn budget (default 10). The simulator knows it and wraps up before running out. |
| `stop_when` | A plain-language stop condition the simulator checks after every agent turn: "the agent has given a clear yes/no answer". |
| `script` | Deterministic steps played before the persona takes over: `say` sends exact text on any conversational target (a recorded conversation, a fixed opening); `wait_for` and `dtmf` are for sip and livekit, for IVR menus. |

The conversation stops when the simulator says the goal or the stop condition is met (or that
the agent cannot help), when the persona hangs up, when the agent ends the session
(`response.done` for rest), when `max_turns` is reached, or when a script with no goal has played
out. `result.json` records which (`stop_reason`).

## Script steps

A `script` plays before the persona takes over (or instead of it, when the scenario has no
`goal`): the same words every run, so a recorded conversation, a fixed opening or an IVR menu is
reproduced exactly.

| Step | Meaning |
|---|---|
| `say: "text"` | Sends exactly this text on any conversational target. Templates render at send time, so `{{vars.x}}` and values captured earlier in the conversation (`{{captured.order_id}}`) work. |
| `dtmf: "1234#"` | Presses keypad digits (sip, livekit audio, twilio): `0`-`9`, `*`, `#`. |
| `wait_for: "text"` | Waits until the agent's transcript contains the text, fuzzily (case, punctuation and spelled digits do not matter), up to half the test timeout; a step that never hears it fails the run with `stop_reason: script`. |

```yaml
tests:
  - name: order status, then a human
    target: api
    capture:
      - { name: order_id, regex: "(ORD-[0-9]+)" }
    scenario:
      script:
        - say: "I placed an order yesterday for the blue sneakers in a 42"
        - say: "What is the status of {{captured.order_id}}?"
        - say: "Can I speak to a person about it?"
    assert:
      - handoff: true

  - name: through the IVR to billing
    target: phone
    scenario:
      script:
        - wait_for: "press 2 for billing"
        - dtmf: "2"
        - wait_for: "how can I help"
      goal: "Ask why last month's invoice is higher than usual"
      max_turns: 6
    assert:
      - transferred_to: { matches: "^sip:billing@" }
```

A script with a `goal` hands over to the simulated user once the last step has played; one
without a goal needs no simulator model at all, which is how imported conversations and the
fake-free CI suites run.

## Voices, behaviours and seeds

`voice` is a hint the speech layer maps to the provider's voices: `<language>-<gender>` such as
`en-GB-female`, `es-ES-male` or just `female` picks the provider's default voice of that gender
(Gemini `Kore`/`Puck`, ElevenLabs `Rachel`/`Adam`, OpenAI `nova`/`onyx`); a provider voice name
or ID (`Puck`, an ElevenLabs or Cartesia voice ID) passes through. The hint's language is also
the TTS language when `speech.tts.language` is not set.

The behaviours shape the caller's audio, in livekit audio mode and on sip calls (and
`interrupt_probability` on realtime, pipecat and twilio too):

```yaml
persona:
  name: Alice
  style: "impatient, cuts in"
  voice: en-GB-female
  behaviors:
    interrupt_probability: 0.5        # half the turns start before the agent has finished
    silence_after_turn_ms: [200, 900] # a short, variable pause before answering
    background_noise: street          # cafe, office, street, white, or a .wav next to the suite
    background_noise_snr_db: 12       # louder than the default 20 dB under the voice
    line_quality: mobile_poor         # pstn (8 kHz μ-law), mobile_poor (loss and jitter), voip_good
```

`seed` on the test fixes the random choices (when to interrupt, how long to pause, which noise
segment plays), so a failing run can be reproduced; `repeat` with different seeds explores the
space. The results carry what happened: `interruptions`, `interruption_stop_ms` (how long the
agent kept talking after being cut off), `talk_over_ratio`, `user_speech_ms` and
`agent_speech_ms`, plus `interrupting`, `interruption` and `interruption_stopped` events with
their timestamps (see [Metrics](/docs/metrics/)).

## One test, several languages

A bilingual agent is tested one language at a time; `languages` runs the same test in each:

```yaml
tests:
  - name: refund policy question
    target: api
    languages: [en, es, de]
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario: { goal: "Find out if you can return shoes bought 40 days ago", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day return policy, in the customer's language"
      - similar_to: "{{vars.language_hint}}"
```

The test becomes `refund policy question [en]`, `[es]` and `[de]`, each tagged `lang:<code>`
(`--tags lang:es` runs one), with the persona speaking that language and `{{vars.language}}` set
to it. The judges are told the language, so a correct answer in Spanish is not marked down for
not being English. `languages` combines with `cases`: a parameterised test runs once per case
and language.

## When the tools fail

An agent is only as good as its worst day with its tools. `fuzz_tools` on a test replaces what
the named tools answer, for that test only, so you can see the agent cope (or not) with a
failing backend. It applies to the targets where tamesu answers the agent's tool calls:
`realtime`, `pipecat`, `model` and `mcp`. The mode may be a template (`"{{vars.mode}}"`), so
`cases` can sweep the modes.

```yaml
tests:
  - name: the order lookup is down
    target: prompt
    fuzz_tools:
      lookup_order: error                       # {"error": "internal error ...", "code": 503}
      get_balance: { mode: delay, delay: 8s }   # the normal answer, 8 seconds late
    scenario: { goal: "Ask where order ORD-77 is", max_turns: 4 }
    assert:
      - pack: graceful_degradation
      - not_contains: "ORD-77 has shipped"       # nothing invented
```

| Mode | The tool answers |
|---|---|
| `error` | `{"error": "internal error: the service is unavailable", "code": 503}` |
| `empty` | `{}` |
| `null` | `null` |
| `malformed` | a truncated JSON document, as a string |
| `hostile` | a result that carries instructions for the agent (an injection through the tool channel) |
| `huge` | a 200 000 character string |
| `wrong_type` | `42` where a document was expected |
| `delay` | the configured result, after `delay` |
| `custom` | `result`, as given |

`delay` combines with any mode. A fuzzed call is a `tool_fuzz` event in `result.json`
(`lookup_order: error`), and a tool with no configured result is answered too. The
`graceful_degradation` pack grades the reaction: no invented data, no instruction followed from a
tool result, no raw error shown, and a next step for the user. `cases` turn one test into a
sweep: `fuzz_tools: { lookup_order: "{{vars.mode}}" }` with a case per mode.

## Templates

Persona and scenario strings take `{{vars.x}}`, `{{env.X}}`, `{{uuid}}` and `{{faker.*}}`:

```yaml
scenario:
  goal: "Log in as {{vars.user}} and ask for the invoice of order {{faker.uuid}}"
```

A `{{secrets.X}}` in a persona or scenario is left as the placeholder: models never see secret
values.

## Repeatability

`seed` is passed to the simulator with every call so the provider can make its sampling
repeatable where it supports that, and the persona's random behaviours (interruptions,
silences) draw from a generator seeded with it. Judges run at temperature 0. Real agents are
still non-deterministic; `repeat` with a `pass_threshold` is how a test states how much
variance it tolerates.

## In the Go API

```go
tamesu.Test{
    Persona:  tamesu.Persona{Name: "Alice", Style: "impatient", Voice: "en-GB-female"},
    Scenario: tamesu.Scenario{Goal: "Move Friday booking to Saturday", MaxTurns: 12},
}
```
