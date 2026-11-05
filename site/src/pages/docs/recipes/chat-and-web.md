---
layout: ../../../layouts/DocsLayout.astro
title: "Recipes: Chat and web agents"
---

# Chat and web agents

A REST agent gated for release, a web shop in the browser, the backend behind the transcript, and escalation.

Each recipe is a whole project you can copy, change the URLs and run; see [Recipes](/docs/recipes/) for the other pages.

## A support agent over REST, gated for release

The project: models, a budget, the baseline limits, a PII pack and SLOs on every test, a hook
that resets the agent's conversation store, and the GitHub reporter for pull requests.

```yaml
# tamesu.yaml
version: 1
suites: ["tests/**/*.yaml"]
genkit:
  plugins: [googlegenai]
models:
  default: googleai/gemini-2.5-flash
  judge: googleai/gemini-2.5-pro
budgets: { max_cost_usd: 3 }
defaults:
  timeout: 2m
  retries: 1
  assert:
    - pack: pii_leakage
    - not_contains: "as an AI language model"
  slo:
    turn_latency_ms_p95: 3000
    time_to_first_token_ms_p95: 1500
    llm_cost_usd: 0.05
baseline:
  dir: .tamesu/baseline
  max_latency_increase_pct: 20
hooks:
  before_each:
    - http: { url: "${ADMIN_URL:-http://localhost:8080}/sessions/reset", method: POST }
reporters: [console, junit, github]
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  API_TOKEN: env:API_TOKEN
```

The suite: one parameterised test over the return window, a scripted conversation that reuses a
captured order id, a golden transcript, and a tool-order check.

```yaml
suite: support
tags: [support]
vars: { base_url: "${AGENT_URL:-http://localhost:8080}" }
targets:
  api:
    type: rest
    url: "{{vars.base_url}}/chat"
    headers: { Authorization: "Bearer {{secrets.API_TOKEN}}" }
    request: { body: { session_id: "{{session}}", message: "{{message}}" } }
    response: { text: "$.reply", tool_calls: "$.tool_calls[*]", done: "$.end_session" }
tests:
  - name: return window
    target: api
    cases:
      - { name: late, vars: { days: "40", expect: "30 days" } }
      - { name: early, vars: { days: "10", expect: "30 days" } }
    persona: { name: Marta, style: polite, language: en }
    scenario: { goal: "Ask whether shoes bought {{vars.days}} days ago can be returned", max_turns: 4 }
    assert:
      - contains: "{{vars.expect}}"
      - judge: "The agent states the 30-day return policy and does not promise an exception"
      - tool_sequence: { calls: [lookup_policy] }

  - name: order status with a captured id
    target: api
    capture:
      - { name: order_id, regex: "order (ORD-[0-9]+)" }
    persona: { name: Ken, style: in a hurry }
    scenario:
      goal: "Place an order for blue sneakers in a 42, then ask when it arrives"
      max_turns: 6
      script:
        - say: "I'd like the blue sneakers in a 42, please"
        - say: "When does order {{captured.order_id}} arrive?"
    assert:
      - captured: { name: order_id, matches: "^ORD-" }
      - golden: { file: golden/order-status.json, mode: judge }
      - metric: { name: turn_latency_ms_p95, lt: 2500 }
```

Run it against the baseline on every pull request, and refresh the baseline from a green main:

```sh
tamesu run --baseline                     # exit 1 on a new failure or a slower p95
tamesu baseline save .tamesu/runs/<ts>    # after a green run on main
```

## A web shop in the browser: mocks, accessibility, visual baseline

```yaml
# tamesu.yaml
version: 1
suites: ["shop.yaml"]
genkit:
  plugins: [googlegenai]
models:
  default: googleai/gemini-2.5-flash
  browser_agent: googleai/gemini-2.5-pro
defaults: { max_goal_steps: 15 }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  TEST_PASSWORD: env:TEST_PASSWORD
```

```yaml
suite: shop
targets:
  web:
    type: browser
    url: "${WEBAPP_URL:-http://localhost:8090}"
    browser:
      viewport: 1280x800
      recording: webm
      mocks:
        - { url: "**/api/recommendations", body: { items: [] } }
        - { url: "**/analytics.js", abort: true }
tests:
  - name: log in, search and add to cart
    target: web
    steps:
      - goal: "Log in as marta with the password {{secrets.TEST_PASSWORD}}"
      - expect: "The page greets Marta by name"
      - action: { a11y: { impact: serious } }
      - goal: "Search for blue sneakers and add the first result in size 42 to the cart"
      - action: { assert_text: { selector: "#cart-count", contains: "1" } }
      - action: { visual: { name: cart, threshold: 0.01, mask: ["#date", ".avatar"] } }
    assert:
      - judge: "The steps show a successful login and one item in the cart"
      - metric: { name: goal_steps, lt: 12 }

  - name: checkout survives a slow payment service
    target: web
    steps:
      - action: { mock: { url: "**/api/payment", method: POST, status: 502, delay_ms: 2000 } }
      - goal: "Open the cart and pay"
      - expect: "The page says payment is unavailable and offers to retry"
```

## Verify the backend, not just the transcript

The agent said the refund is done; was it? `capture` takes the order id from the agent's words,
a hook resets the fixture before each run, and two state assertions ask the backend after the
conversation: an HTTP check against the admin API and a shell check against the ledger.

```yaml
# tamesu.yaml
version: 1
suites: ["refunds.yaml"]
genkit: { plugins: [googlegenai] }
models: { default: googleai/gemini-2.5-flash }
vars: { admin_url: http://localhost:8081 }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ADMIN_TOKEN: env:ADMIN_TOKEN
hooks:
  before_each: [ { http: { url: "{{vars.admin_url}}/test/reset", method: POST, headers: { Authorization: "Bearer {{secrets.ADMIN_TOKEN}}" } } } ]
```

```yaml
suite: refunds
targets:
  api:
    type: rest
    url: "${AGENT_URL:-http://localhost:8080/chat}"
    response: { text: "$.reply", tool_calls: "$.tool_calls[*]" }
tests:
  - name: a damaged pair is refunded
    target: api
    persona: { name: Marta, style: "polite, has her order number ready" }
    scenario:
      goal: "Get a refund for order ORD-1001: the shoes arrived with a torn sole"
      max_turns: 6
    capture:
      - { name: order_id, regex: "(ORD-[0-9]+)" }
    assert:
      - judge: "The agent confirms the refund and gives a timeline"
      - tool_called: { name: issue_refund, args: { order_id: ORD-1001 } }
      - http:
          url: "{{vars.admin_url}}/orders/{{captured.order_id}}"
          headers: { Authorization: "Bearer {{secrets.ADMIN_TOKEN}}" }
          json_path: { path: "$.status", equals: refunded }
      - http:
          url: "{{vars.admin_url}}/orders/{{captured.order_id}}/events"
          regex: '"type":"refund_issued"'
      - command: { run: "./scripts/ledger.sh {{captured.order_id}}", contains: credited, timeout: 20s }

  - name: a refund outside the window is not issued
    target: api
    persona: { name: Ken, style: insistent }
    scenario: { goal: "Get a refund for order ORD-1002, delivered 40 days ago", max_turns: 6 }
    capture:
      - { name: order_id, regex: "(ORD-[0-9]+)" }
    assert:
      - judge: "The agent refuses the refund, explains the 30-day window and offers the voucher"
      - tool_not_called: issue_refund
      - http: { url: "{{vars.admin_url}}/orders/{{captured.order_id}}", json_path: { path: "$.status", equals: delivered } }
```

```sh
ADMIN_TOKEN=... tamesu run
```

The state checks run once the conversation ends; their result quotes the response or the output
as evidence, and `{{captured.order_id}}` is whatever the agent actually said, so a wrong order id
fails the right way.

## Escalation: when the agent must hand off, and when it must not

The target says how this agent escalates (a tool call here; `json_path` or `regex` for agents
that signal it otherwise), the tests say when it should and should not, and `discloses_ai`
makes sure it never claims to be a person while doing it.

```yaml
suite: escalation
tags: [handoff]
targets:
  api:
    type: rest
    url: "${AGENT_URL:-http://localhost:8080/chat}"
    response: { text: "$.reply", tool_calls: "$.tool_calls[*]", done: "$.end_session" }
    handoff: { tool: transfer_to_human, to: department, summary: summary }
tests:
  - name: an angry customer reaches a person quickly
    target: api
    persona: { name: Ray, style: "furious, swears every turn, repeats himself" }
    scenario: { goal: "Complain about a parcel that is two weeks late until a human takes over", max_turns: 6 }
    assert:
      - handoff: { within_turns: 3, to: "^support$", summary_contains: ["late"], summary: "The summary says what the customer is angry about and what was promised" }
      - pack: discloses_ai
      - metric: { name: handoff_turn, lte: 3 }

  - name: asking for a human is honoured
    target: api
    scenario:
      script: [ { say: "I want to talk to a real person, not a bot." } ]
    assert:
      - handoff: true
      - pack: discloses_ai

  - name: a simple question stays with the agent
    target: api
    persona: { name: Marta, style: polite }
    scenario: { goal: "Ask how long shipping takes", max_turns: 3 }
    assert:
      - handoff: false
      - contains: "business days"

  - name: no handoff for things the agent can do
    target: api
    persona: { name: Ken, style: "a little impatient" }
    scenario: { goal: "Change the delivery address of order ORD-1001 to 5 Rue Vert, Lyon", max_turns: 5 }
    assert:
      - handoff: false
      - tool_called: update_address
```

```sh
tamesu run --tags handoff
tamesu coverage .tamesu/runs/<latest>          # stop reasons: how many conversations ended in a handoff
```

The conversation stops when the signal fires, with stop reason `handoff`; a SIP transfer
(`transferred_to`) counts as a handoff to its target, so the same assertions work on a phone line.
