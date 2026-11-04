---
layout: ../../../layouts/DocsLayout.astro
title: Voiceflow target
---

# Voiceflow target

![A round robot walking a flowchart path laid on the floor, handing a cloud-shaped speech bubble to a person with a clipboard](/img/target-voiceflow.jpg)

The `voiceflow` target tests an agent built on **Voiceflow** through its Conversations API: the
same calls the web chat, the API integrations and the test console make. tamesu launches the
agent, sends each user turn as a text action and reads the agent's turn from the traces it
answers: its text and speak messages, the buttons and cards it offers, the tools it called, a
handoff to a live agent, the end of the conversation. The agent runs exactly as it does in
production, against the draft or the published build of an environment.

```yaml
targets:
  support:
    type: voiceflow
    api_key: "{{secrets.VF_TOKEN}}"                  # a personal or service token
    project_id: "{{vars.project_id}}"
    environment: main                                # the environment alias (default main)
    version: published                               # draft | published
    variables: { customer_name: Marta, tier: gold }  # set before the launch
    user_id: "tamesu-{{uuid}}"                       # one conversation per test (default)
tests:
  - name: return after 40 days
    target: support
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario: { goal: "Find out if the shoes bought 40 days ago can still be returned", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day window and does not promise an exception"
      - json_path: { path: "$[?(@.type == 'choice')]" }          # the first reply offered buttons
        on_turn: 1
      - not_contains: "[buttons: Talk to a human"
```

## How a conversation runs

1. With `reset: true`, the conversation's state is deleted first (`DELETE
   /conversation-state/{userID}`), for a `user_id` that is reused; the default id is fresh per test.
2. `variables` are set on the conversation (`PATCH /conversation-state/{userID}/variables`), so the
   agent knows the caller from its first words.
3. The agent is launched (`PUT /conversation/{userID}` with `action: launch` and `version`). Its
   opening words are the greeting: recorded as an event and read with the first turn, as a
   person would hear them before answering.
4. Each user turn is one `text` action. The traces of the answer become the agent's turn:
   `text` and `speak` messages are its words, one per line; `choice` buttons become
   `[buttons: Yes | No]`, a `cardV2` becomes `[card: title: description]` and a `carousel`
   `[carousel: title | title]`, so the simulated user and the judge see what was offered;
   `completion` content is kept when the runtime streams it; `tool-call` traces are the turn's
   tool calls when they name a tool, and `tool_call` events always; `live-agent-handoff` is a
   `live_agent_handoff` event; `end` ends the session; `no-reply` is a `no_reply` event. Debug,
   log, block, flow and path traces stay out of the text but in `result.json`.
5. A `dtmf` script step sends a `dtmf` action, for voice agents tested over text.

Every trace of a turn is in `result.json` (the first turn also carries the launch's), so
`json_path` reaches buttons, cards, paths and debug messages. Requests the API refuses (a 401, a
400 for an unknown environment) fail the session with the API's message.

## The legacy Dialog Manager API

Projects still integrated through `general-runtime.voiceflow.com` with a Dialog API key use
`version_id` instead of `project_id`: the target then posts to `/state/user/{userID}/interact`
with the key in `Authorization` and the version in `versionID`, variables in the launch's
`state`, and reads the trace array the same way.

```yaml
targets:
  legacy:
    type: voiceflow
    api_key: "{{secrets.VF_DM_KEY}}"
    version_id: production                           # or development, or a version id
```

## Options

| Field | Meaning |
|---|---|
| `api_key` | A personal or service token (REST API), or a Dialog API key (legacy), as `{{secrets.NAME}}`. |
| `project_id` | The project, on the REST API (templates allowed). |
| `environment` | The environment alias, default `main`. |
| `version` | `published` (default) or `draft`. A project that was never published has no published build: use `draft`. |
| `version_id` | The legacy API's `versionID` (`production`, `development` or an id); selects the legacy API when `project_id` is not set. |
| `user_id` | The conversation id template, default `tamesu-{{uuid}}`. |
| `variables` | Variables set on the conversation before the launch (templates allowed). |
| `reset` | Delete the conversation's state before the launch. |
| `base_url` | The API, default `https://realtime-api.voiceflow.com/v1/stable` (REST) or `https://general-runtime.voiceflow.com` (legacy). |
| `turn_timeout` | How long one request may take, default `60s`. |
| `connect` | How a request that never reached the API is retried, as for every target. |

## Metrics and events

`turn_latency_ms` and `turns` as for every text target, `http_requests` (reset, variables, launch
and one per turn) and `voiceflow_traces` (every trace read). Events: `reset`, `variables`,
`launch`, `greeting`, `tool_call`, `live_agent_handoff`, `no_reply`, `end`, and `trace` for a
trace type the target does not know.

## Assertions that apply

Every assertion of [Concepts](/docs/concepts/#assertions) except the voice ones: `judge`,
`contains`, `regex`, `similar_to`, `tool_called` when the runtime names the tool, `grounded`
with a `text` or `json_path` context, `json_path` over the traces, `captured`, `golden`,
`metric`, `turns`, `http` and `command`, and the packs. Recorded Voiceflow transcripts can also
be turned into suites with `tamesu import --format voiceflow`.
