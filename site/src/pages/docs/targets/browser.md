---
layout: ../../../layouts/DocsLayout.astro
title: Browser target
---

# Browser target

The `browser` target tests a web app the way a person would: Chromium (headless by default) over
the DevTools protocol, driven step by step. A `goal` step hands the **browser agent** a sentence
in plain language and lets it act until the goal is met; an `expect` step asks the model whether
the page satisfies a sentence; an `action` step is deterministic (click this, fill that, assert
the URL). Every step leaves a screenshot, and every session a HAR and the console log.

```yaml
targets:
  web:
    type: browser
    url: ${APP_URL}
    browser: { engine: chromium, viewport: 1280x800, locale: en-US }
    auth: { storage_state: ./auth/state.json }     # optional: cookies and localStorage to start from
    allowed_domains: [shop.example.com]            # default: the url's host

tests:
  - name: checkout happy path
    target: web
    steps:
      - goal: "Log in as {{vars.user}} with password {{secrets.TEST_PASSWORD}}"
      - goal: "Search for 'laptop' and add the first result to the cart"
      - expect: "The cart badge shows 1 item"
      - action: { navigate: "${APP_URL}/cart" }
      - action: { assert_text: "1 item" }
    assert:
      - judge: "The user reached a cart with one item"
      - metric: { name: goal_steps, lte: 12 }
```

## Chromium

tamesu uses the binary in `TAMESU_CHROMIUM` when set, else Chrome, Chromium or Edge from the
usual locations and the `PATH`, else it downloads Chrome for Testing (stable) into
`~/.cache/tamesu/chromium` on first use. `tamesu doctor` reports which one it found. The Docker
image `ghcr.io/xavidop/tamesu` ships Chromium. `tamesu run --headed` shows the window.

## How a goal runs

1. **Observe.** The page becomes an accessibility tree, one line per element the agent can read
   or act on: `[12] button "Add to cart"`, `[13] textbox "Search products" value="laptop"`,
   `    heading "Your cart" (h1)`. Refs are stable for an element across observations. The URL,
   the title and the last console errors come with it.
2. **Plan.** The `browserStep` flow (model `models.browser_agent`, else `models.default`) gets
   the goal and the observation and calls one tool at a time: `click(ref)`, `type(ref, text)`,
   `select(ref, value)`, `scroll(direction)`, `navigate(url)`, `wait(ms)`, `done(success, reason)`.
3. **Act.** Each tool runs in the browser and answers with the new page. The loop ends when the
   agent calls `done`, or after `max_goal_steps` actions (default 15), which fails the step.

Secrets never reach the model: the goal keeps `{{secrets.NAME}}` and the agent types the
placeholder literally; the browser substitutes the value at the moment of typing. The agent is
told the allowed domains and the page refuses any navigation outside them (a `navigation_refused`
event). `expect` runs the `checkExpect` flow over the same observation and answers `{ pass,
reason }`.

## Deterministic actions

| Action | Argument | Does |
|---|---|---|
| `navigate` | a URL | Loads it (within `allowed_domains`). |
| `click` | a CSS selector, or a ref | Clicks it. |
| `fill` | `{ selector or ref, text }` | Clears the field and types `text` (secrets rendered). |
| `press` | `Enter`, `Tab`, `Escape`, `Backspace`, `Arrow*`, a character | Sends the key. |
| `wait` | milliseconds, `{ selector }` or `{ text }` | Pauses, or waits for an element or text. |
| `screenshot` | a name | Saves `screenshots/<name>.png`. |
| `eval_js` | an expression | Runs it; the value goes in the step record. |
| `assert_url` | a string (contains) or `{ contains, equals, matches }` | Fails the step when the URL does not match. |
| `assert_text` | a string, or `{ contains, selector }` | Fails the step when the page (or element) text does not contain it. |

A failed `assert_*` or `expect`, or a goal the agent gives up on, fails the test as a `step[N]`
assertion with the reason; a missing element or a browser error is an error (exit 3).

## Artifacts and metrics

Each run directory has `screenshots/step-NN.png` (one per step), `steps.json` (kind, text,
success, reason, the agent's actions, timings), `trace.har` (every request, HAR 1.2), and
`console.log` (console errors and uncaught exceptions). `transcript.json` holds the steps as a
conversation (each step's text, then its outcome), so `judge`, `contains` and `regex` assertions
apply. Metrics: `goal_steps` (agent actions over all goals), `requests`, `console_errors`,
`llm_cost_usd`, `llm_tokens`.

## Example

[`examples/browser-shop`](https://github.com/xavidop/tamesu/tree/main/examples/browser-shop) tests
[`examples/webapp`](https://github.com/xavidop/tamesu/tree/main/examples/webapp), a tiny shop
(login, search, cart) with no dependencies. `make integration` runs it headless with the fake
browser agent replaying scripted tool calls against the real Chromium; with a model
(`tamesu.yaml` there) the agent finds its own way.
