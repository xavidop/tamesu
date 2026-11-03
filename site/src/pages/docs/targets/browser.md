---
layout: ../../../layouts/DocsLayout.astro
title: Browser target
---

# Browser target

![A browser window with a pointer about to click a button and a magnifying glass over the page](/img/target-browser.jpg)

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
2. **Plan.** The browser agent (model `models.browser_agent`, else `models.default`) gets the
   goal and the observation and calls one tool at a time: `click(ref)`, `type(ref, text)`,
   `select(ref, value)`, `scroll(direction)`, `navigate(url)`, `wait(ms)`, `done(success, reason)`.
3. **Act.** Each tool runs in the browser and answers with the new page. The loop ends when the
   agent calls `done`, or after `max_goal_steps` actions (default 15), which fails the step.

Secrets never reach the model: the goal keeps `{{secrets.NAME}}` and the agent types the
placeholder literally; the browser substitutes the value at the moment of typing. The agent is
told the allowed domains and the page refuses any navigation outside them (a `navigation_refused`
event). `expect` asks the same model one question over the same observation and gets `{ pass,
reason }` back.

## The action cache

Every goal the agent achieves is saved as a **plan**: its actions with the element each one
acted on (`button "Log in"`), under `.tamesu/cache/actions/<key>.json` (`cache.dir` in
`tamesu.yaml`). The key is the goal text, the URL path and a fingerprint of the page's
interactive elements, so the same goal on a changed page plans again. On the next run the plan
is replayed first, matching each element on the fresh page by role and name; the agent runs
only on a miss or when a replay fails (and the plan is refreshed). That is what keeps CI fast
and cheap: a stable app replays with no model calls for its goals.

- `tamesu run --replay` is strict: cached plans only, a miss or a failed replay fails the step.
- `tamesu run --update-cache` ignores the cache, plans again and refreshes it.
- `cache_hits` and `cache_misses` are metrics of every browser run; `cache_hit`, `cache_miss`,
  `cache_replay_failed` and `cache_saved` are events in `result.json`. Commit the cache
  directory to replay in CI, or keep it as a CI cache keyed on the suite.

## Recording steps

`tamesu record browser <url>` opens a window on the URL and turns what you do into `action:`
steps with durable selectors (an id, a `name`, an `aria-label`, else a short path), passwords as
`{{secrets.PASSWORD}}`. Close the browser to get the `steps:` block, paste it into a test and
replace the brittle parts with `goal` steps where a person would rather say what they want.

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
| `mock` | `{ url, method, status, body, headers, delay_ms, abort }` | From now on, answers matching requests instead of the network (see [Mocking requests](#mocking-requests)). |
| `a11y` | `{ impact: serious, max: 0, include: selector }` | Runs an accessibility audit on the page and fails the step when violations at or above `impact` exceed `max` (see [Accessibility](#accessibility)). |
| `visual` | a name, or `{ name, threshold: 0.01, mask: [selectors] }` | Compares a screenshot with the baseline image of that name and fails the step when more than `threshold` of the pixels differ (see [Visual regression](#visual-regression)). |

A failed `assert_*` or `expect`, or a goal the agent gives up on, fails the test as a `step[N]`
assertion with the reason; a missing element or a browser error is an error (exit 3).

## Mocking requests

`browser.mocks` on the target answers matching requests before the page ever reaches the
network, from the first navigation; the `mock` action adds more during a test, the latest
first. A mock without `abort` answers with `status` (default 200), `body` (a string as is, any
other value as JSON with its content type) and `headers`, after `delay_ms`; `abort` fails the
request instead. `url` is a glob on the full URL (`**` crosses slashes) or, without a wildcard,
a substring; `method` narrows it.

```yaml
targets:
  web:
    type: browser
    url: https://shop.example.com
    browser:
      mocks:
        - { url: "**/api/recommendations", body: { items: [] } }
        - { url: "**/api/payment", method: POST, status: 502, delay_ms: 3000 }
        - { url: "**/analytics.js", abort: true }
tests:
  - name: checkout survives a slow payment service
    target: web
    steps:
      - action: { mock: { url: "**/api/cart", body: { items: 3 } } }
      - goal: "Open the cart and pay"
      - expect: "The page says payment is unavailable and offers to retry"
```

Mocked requests are marked `_mocked` in `trace.har`, counted in `mocked_requests`, and each one
is a `mock` event on the timeline.

## Accessibility

The `a11y` action audits the page with [axe-core](https://github.com/dequelabs/axe-core) and
fails the step when the violations at or above `impact` (`minor`, `moderate`, `serious` by
default, `critical`) outnumber `max` (default 0); `include` limits the audit to a selector. Every
audit writes `a11y-<n>.json` into the run directory with all the violations, their rules and
node counts, and `a11y_violations` sums the counted ones.

```yaml
steps:
  - goal: "Open the checkout page"
  - action: { a11y: { impact: serious } }        # no serious or critical violations
  - action: { a11y: { impact: minor, max: 5, include: "main" } }
```

## Visual regression

The `visual` action screenshots the page and compares it with the baseline `visual/<name>.png`
next to the suite. The first run writes the baseline and passes with a note; later runs fail the
step when more than `threshold` (default `0.01`, one percent) of the pixels differ, writing
`visual/<name>-diff.png` (the differences in red) and `visual/<name>-actual.png` into the run
directory. `mask` blanks the elements that change on purpose, such as dates or avatars, on both
sides before comparing. When the new look is the intended one, `tamesu run --update-visual`
rewrites the baselines; commit them with the suite.

```yaml
steps:
  - action: { navigate: "{{vars.base_url}}/pricing" }
  - action: { visual: { name: pricing, threshold: 0.005, mask: ["#live-chat", ".date"] } }
```

The metric `visual_diffs` counts the failed comparisons. Keep the viewport fixed
(`browser.viewport`) so baselines compare like with like.

## Artifacts and metrics

Each run directory has `screenshots/step-NN.png` (one per step), a recording of the whole
session, `steps.json` (kind, text, success, reason, the agent's actions, timings), `trace.har`
(every request, HAR 1.2), and `console.log` (console errors and uncaught exceptions).
`transcript.json` holds the steps as a conversation (each step's text, then its outcome), so
`judge`, `contains` and `regex` assertions apply. Metrics: `goal_steps` (agent actions over all
goals), `requests`, `console_errors`, `recording_frames`, `mocked_requests`, `a11y_violations`,
`visual_diffs`, `llm_cost_usd`, `llm_tokens`.

The recording is every frame Chromium painted, with its real timing, scaled to 960 px wide:
`recording.webm` when `ffmpeg` is installed, otherwise `recording.gif`. `browser.recording`
on the target chooses: `auto` (the default), `webm`, `gif` or `off`. The HTML report plays it
above the step screenshots.

```yaml
targets:
  web:
    type: browser
    url: https://shop.example.com
    browser: { viewport: 1280x800, recording: gif }
```

## Example

[`examples/browser-shop`](https://github.com/xavidop/tamesu/tree/main/examples/browser-shop) tests
[`examples/webapp`](https://github.com/xavidop/tamesu/tree/main/examples/webapp), a tiny shop
(login, search, cart) with no dependencies. With a model (`tamesu.yaml` there) the agent finds
its own way; with the fake provider (`tamesu.llmfake.yaml`) it replays scripted actions against
the real Chromium.
