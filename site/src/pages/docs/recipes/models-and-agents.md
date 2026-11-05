---
layout: ../../../layouts/DocsLayout.astro
title: "Recipes: Models, tools and agents"
---

# Models, tools and agents

A prompt and its tools against a model, an MCP server through the model that uses it, and an orchestrator with the agents it delegates to.

Each recipe is a whole project you can copy, change the URLs and run; see [Recipes](/docs/recipes/) for the other pages.

## A prompt and its tools against a model, grounded and under failure

No agent server yet: the system prompt and its tool definitions are tested directly against a
model. The tools answer from the results in the suite, so the test knows exactly what the model
was told; `grounded` checks it said nothing beyond that, and `fuzz_tools` with `cases` runs the
same conversation once per failure mode to see the prompt cope.

```yaml
# tamesu.yaml
version: 1
suites: ["prompt.yaml"]
genkit:
  plugins: [googlegenai, anthropic]
models:
  default: googleai/gemini-2.5-flash           # the model the prompt runs on
  simulator: googleai/gemini-2.5-flash
  judge: anthropic/claude-sonnet-4-6
budgets: { max_cost_usd: 2 }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  ANTHROPIC_API_KEY: env:ANTHROPIC_API_KEY
```

```yaml
suite: support-prompt
tags: [prompt]
targets:
  prompt:
    type: model
    system_file: prompts/support.md             # the prompt under test, next to the suite
    fallback: [googleai/gemini-2.5-pro]        # when the default model keeps failing
    tools:
      - name: lookup_policy
        description: The store policy for a topic
        parameters: { type: object, properties: { topic: { type: string } }, required: [topic] }
        result: { topic: "{{args.topic}}", window_days: 30, exceptions: none, voucher_after: "10%" }
      - name: lookup_order
        description: An order by id
        parameters: { type: object, properties: { id: { type: string } }, required: [id] }
        result: { id: "{{args.id}}", status: delivered, delivered_days_ago: 40 }
      - name: end_session
        description: Call when the customer has no more questions
tests:
  - name: return after 40 days
    target: prompt
    persona: { name: Marta, style: "polite, slightly confused" }
    scenario:
      goal: "Find out if order ORD-1002, delivered 40 days ago, can still be returned"
      max_turns: 6
    assert:
      - tool_called: { name: lookup_policy, args: { topic: returns } }
      - tool_sequence: { calls: [lookup_order, lookup_policy], exact: false }
      - grounded: { context: { tool: lookup_policy } }       # nothing the policy did not say
      - judge: "The agent states the 30-day window, offers the voucher and promises no exception"
      - not_contains: "As an AI"

  - name: the tools misbehave
    target: prompt
    cases:                                     # one test per failure mode
      - { name: error, vars: { mode: error } }
      - { name: empty, vars: { mode: empty } }
      - { name: hostile, vars: { mode: hostile } }
      - { name: slow, vars: { mode: delay } }
    fuzz_tools:
      lookup_order: "{{vars.mode}}"
    persona: { name: Ken, style: "in a hurry" }
    scenario: { goal: "Ask where order ORD-1001 is", max_turns: 4 }
    assert:
      - pack: graceful_degradation             # no invented data, no raw error, a next step
      - not_contains: "ORD-1001 has shipped"
      - metric: { name: turn_latency_ms_max, lt: 60000 }
```

```sh
tamesu run --tags prompt                       # 5 tests: the policy question and four failure modes
tamesu run --matrix models.default=googleai/gemini-2.5-flash,openai/gpt-4.1-mini   # the same suite on two models, compared
```

`delay` holds the normal answer back (8 seconds by default when `fuzz_tools` names only the mode;
`{ mode: delay, delay: 8s }` sets it); the other modes replace the answer. Every fuzzed call is a
`tool_fuzz` event in `result.json`.

## An MCP server, through the model that uses it

The server is the thing under test: its tools are offered to a model and every call runs on the
server, so the suite sees the real descriptions, schemas, answers and errors. `allow_tools`
narrows what the model gets; `tamesu coverage` lists the tools that were never called.

```yaml
# tamesu.yaml
version: 1
suites: ["mcp.yaml"]
genkit: { plugins: [googlegenai] }
models: { default: googleai/gemini-2.5-flash, judge: googleai/gemini-2.5-pro }
secrets:
  GEMINI_API_KEY: env:GEMINI_API_KEY
  MCP_TOKEN: env:MCP_TOKEN
```

```yaml
suite: orders-mcp
targets:
  local:
    type: mcp
    command: ./bin/orders-mcp                  # a stdio server, relative to the suite
    args: ["--data", "fixtures/orders.json"]
    env: { LOG_LEVEL: warn }
    system: "You are the order desk. Use the tools for every fact; say so when a tool fails."
    done_tool: end_session
  hosted:
    type: mcp
    url: https://mcp.example.com/mcp           # a streamable HTTP server
    headers: { Authorization: "Bearer {{secrets.MCP_TOKEN}}" }
    allow_tools: [lookup_order, cancel_order]
    call_timeout: 20s
tests:
  - name: the server offers its tools
    target: local
    scenario:
      script: [ { say: "Hello, are you there?" } ]
    assert:
      - metric: { name: mcp_tools, gte: 3 }

  - name: where is my order
    target: local
    persona: { name: Marta, style: polite }
    scenario: { goal: "Find out where order ORD-1001 is and when it arrives", max_turns: 4 }
    assert:
      - tool_called: { name: lookup_order, schema: { type: object, required: [id], properties: { id: { pattern: "^ORD-" } } } }
      - grounded: { context: { tool: lookup_order } }
      - judge: "The agent gives the carrier and the delivery estimate the tool returned"

  - name: an unknown order is reported, not invented
    target: hosted
    persona: { name: Ken, style: insistent }
    scenario: { goal: "Ask about order ORD-9999, which does not exist", max_turns: 4 }
    assert:
      - tool_called: lookup_order
      - pack: graceful_degradation
      - not_regex: '(?i)ORD-9999 (has|was) (shipped|delivered)'
```

```sh
go build -o bin/orders-mcp ./cmd/orders-mcp
tamesu run                                     # spawns the stdio server per session; the hosted one is reached over HTTP
tamesu coverage .tamesu/runs/<latest>          # which tools the model called, and which never
```

A server error (`isError`) reaches the model as `{"error": ...}` and is a `tool_error` event; the
`mcp_tools` metric is how many tools the server listed.

## An orchestrator and the agents it delegates to

An orchestrator that hands work to other agents over A2A is tested without those agents: the
suite declares them as `remote_agents`, tamesu runs them with scripted replies, the orchestrator
is pointed at their URLs, and the assertions say which agent must have been asked and what the
user must hear. A second test makes the billing agent fail and checks the orchestrator copes.

```yaml
suite: orchestrator
tags: [a2a, orchestration]
vars: { orchestrator: "${ORCHESTRATOR_URL:-http://127.0.0.1:8100/a2a}" }
remote_agents:
  billing:
    description: Refunds and invoices
    skills: [refund, invoice-lookup]
    reply: "I can help with refunds and invoices."
    replies:
      - { when: "(?i)refund.*ORD-1001", say: "Refund of 42 euros issued, reference RF-889", data: { reference: RF-889 } }
      - { when: "(?i)refund", say: "Which order should I refund?", state: input-required }
  shipping:
    addr: 127.0.0.1:8096                 # the orchestrator reads SHIPPING_AGENT_URL=http://127.0.0.1:8096 at startup
    skills: [track]
    reply: "ORD-1001 is with the carrier and arrives tomorrow."
targets:
  orchestrator:
    type: a2a
    url: "{{vars.orchestrator}}"
    metadata: { billing_agent_url: "{{remote.billing.url}}" }   # an orchestrator that takes its agents per call
tests:
  - name: a refund goes to billing, not shipping
    target: orchestrator
    persona: { name: Marta, style: "direct, knows her order number" }
    scenario: { goal: "Get a refund for order ORD-1001, delivered damaged", max_turns: 4 }
    assert:
      - delegated_to: { agent: billing, contains: "ORD-1001" }
      - tool_not_called: shipping
      - contains: "RF-889"
      - judge: "The agent tells the user the refund was issued and gives the reference"
      - metric: { name: delegations, lte: 2 }
  - name: billing is down
    target: orchestrator
    fuzz_tools: { billing: error }
    persona: { name: Marta, style: direct }
    scenario: { goal: "Get a refund for order ORD-1001", max_turns: 4 }
    assert:
      - delegated_to: billing
      - pack: graceful_degradation
      - not_contains: "RF-889"
```

Run the orchestrator so it reads its agents' URLs from its configuration, then
`tamesu run --parallel 1` (the shipping agent has a fixed port). Each delegation is in the
transcript's tool calls with the message and the reply, in the `delegated` events, and in
`tamesu coverage` under the tools, so a missing delegation reads as plainly as a missing tool
call. To test against the real billing agent while still recording and fuzzing it, replace its
replies with `forward: https://billing.internal/a2a` and a `headers` entry for its token.
