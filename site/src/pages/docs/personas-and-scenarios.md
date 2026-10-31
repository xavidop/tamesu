---
layout: ../../layouts/DocsLayout.astro
title: Personas and scenarios
---

# Personas and scenarios

A conversational test (rest, livekit, sip) is driven by a simulated user: a **persona** (who is
talking) with a **scenario** (what they want and when to stop). The simulator is a Genkit flow,
`simulateUserTurn`, that reads the persona, the scenario and the conversation so far and
produces the next user turn, or decides to stop.

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
| `behaviors.background_noise` | Audio: a noise preset mixed into the outgoing audio (`cafe`, `street`). |
| `behaviors.line_quality` | Sip: `pstn` (8 kHz G.711), `mobile_poor`, `voip_good`. |

Give the persona what the agent will ask for: a name, an order reference in the goal, a date. The
simulator invents plausible details that fit the persona when it must, and never gives a real
secret, card number or password. The audio behaviours take effect with the audio runners, which
are planned.

## Scenario

| Key | Meaning |
|---|---|
| `goal` | What the persona is trying to achieve, in the second person: "Find out if you can return shoes bought 40 days ago". |
| `max_turns` | The turn budget (default 10). The simulator knows it and wraps up before running out. |
| `stop_when` | A plain-language stop condition the simulator checks after every agent turn: "the agent has given a clear yes/no answer". |
| `script` | Sip and livekit audio: deterministic steps (`wait_for`, `dtmf`, `say`) mixed with free conversation, for IVR menus (planned with the sip runner). |

The conversation stops when the simulator says the goal or the stop condition is met (or that
the agent cannot help), when the persona hangs up, when the agent ends the session
(`response.done` for rest), or when `max_turns` is reached. `result.json` records which
(`stop_reason`).

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
