---
layout: ../../../layouts/DocsLayout.astro
title: Desktop target
---

# Desktop target

The `desktop` target tests a desktop application the way a person would, on macOS, Windows and
Linux. It reads the app through the operating system's accessibility tree, the same tree a
screen reader uses, and acts on the elements it finds there: no screenshots to interpret, no
coordinates. A `goal` step hands the agent a sentence and lets it act until the goal is met; an
`expect` step asks the model whether the window satisfies a sentence; an `action` step is
deterministic (click this, fill that, press a key, assert the text).

```yaml
targets:
  notes:
    type: desktop
    app: TextEdit                       # the application's name or bundle id
    desktop: { args: ["--new-window"] } # optional: arguments for the app

tests:
  - name: write and save a note
    target: notes
    steps:
      - goal: "Write a note that says 'Buy oat milk'"
      - expect: "The document holds the note about oat milk"
      - action: { press: cmd+s }
      - action: { fill: { name: "Save As:", text: "groceries" } }
      - action: { click: { role: button, name: Save } }
      - action: { assert_text: "groceries" }
    assert:
      - judge: "A note was written and saved under the name groceries"
```

## agent-desktop

The app is driven by [agent-desktop](https://github.com/vercel-labs/agent-desktop), a CLI that
speaks to the accessibility API of each system: the AX API on macOS, UI Automation on Windows,
AT-SPI on Linux. It is the target's engine (`desktop.engine: agent-desktop`, the default).

tamesu runs a pinned version of it. On first use it downloads that version into
`~/.cache/tamesu/agent-desktop` and checks the file against the SHA-256 tamesu ships with before
running it. Set `TAMESU_AGENT_DESKTOP` to a binary of your own to use that one instead.
`tamesu doctor` reports which binary a run would use, its version, and the permissions below.

## What the machine needs

A desktop test needs a desktop: the session of a signed-in user, with the app able to open a
window.

| System | What it takes |
|---|---|
| macOS 13 or later | **Accessibility** permission for the application that runs tamesu (the terminal, the editor, the CI agent), in System Settings, Privacy & Security. **Screen Recording** as well for screenshots; without it tests run and the screenshots are left out. |
| Windows 10 (1809) or later | The interactive session of the signed-in user: not a service, not a locked screen. An app running as administrator needs tamesu to run as administrator too. |
| Linux (Ubuntu 24.04 or later) | A graphical session with a window manager and the accessibility bus (`at-spi2-core`), on X11 or Wayland. |

On a Linux machine without a screen, such as a CI runner or a container, start a session first:

```sh
sudo apt-get install -y xvfb openbox dbus x11-utils at-spi2-core libatk-bridge2.0-0
export DISPLAY=:99
Xvfb :99 -screen 0 1280x800x24 -nolisten tcp &
openbox &
dbus-run-session -- tamesu run
```

agent-desktop documents each system in detail:
[macOS](https://agent-browser.dev/desktop/platforms/macos),
[Windows](https://agent-browser.dev/desktop/platforms/windows),
[Linux](https://agent-browser.dev/desktop/platforms/linux).

## How a goal runs

1. **Observe.** The app's window becomes one line per element the agent can read or act on:
   `[12] button "Save"`, `[13] textfield "Title" value="Groceries"`, `    text "3 notes"`. An
   element keeps its number across observations.
   - What is open on top of the window comes after it: an alert, a sheet, a popover, an open
     menu. That is what answers first.
   - A menu lists its items once it is open. The items of a closed menu are left out. On macOS,
     where the menu bar is not part of the window, its titles come last.
   - Elements the app marks hidden are left out.
   - An element marked `(offscreen)` is outside what the window shows. It is scrolled into view
     when it is acted on.
2. **Plan.** The agent (model `models.browser_agent`, else `models.default`) gets the goal and
   the observation and calls one tool at a time: `click(ref)`, `type(ref, text)`,
   `select(ref, value)`, `press(key)`, `wait(ms)`, `done(success, reason)`.
3. **Act.** Each tool runs on the app and answers with the new window. The loop ends when the
   agent calls `done`, or after `max_goal_steps` actions (default 15), which fails the step.

A click presses the element through the accessibility API: the pointer does not move. Text is
set the same way, without a key being pressed. A field that takes text from the keyboard only,
such as the edit box of a classic Windows app, is the exception: it is focused and typed into
with the real keyboard, and the run records a `keyboard` event.
Secrets never reach the model: the goal keeps `{{secrets.NAME}}` and the agent types the
placeholder literally; the value is substituted at the moment of typing and reaches
agent-desktop on standard input, never as an argument.

Achieved goals are saved as plans and replayed on the next run, as on the
[browser target](/docs/targets/browser#the-action-cache): `--replay` and `--update-cache` work
the same way.

## Deterministic actions

| Action | Argument |
|---|---|
| `click` | A ref, an accessible name (`click: "Save"`), or `{ role, name }` |
| `fill` | `{ ref \| name \| role and name, text }`: empties the field, then types |
| `press` | A key or a combination: `Enter`, `Escape`, `Tab`, `cmd+s`, `ctrl+shift+p` |
| `wait` | Milliseconds, or `{ text }` to wait until the window holds a text |
| `screenshot` | A name: the app's window, saved under `screenshots/` |
| `assert_text` | A text the window must hold, or `{ contains \| equals \| matches }` |

Names are matched exactly, without regard to case. An element has to be showing to answer, so
open a menu before clicking one of its items:

```yaml
steps:
  - action: { click: Edit }
  - action: { click: "Select All" }
```

A name alone is the portable way to name a menu: its role differs between systems (the titles
of the menu bar are `menu` on Linux and `menuitem` on macOS). Add the `role` when two elements
share a name. What is open on top of the window is searched first, then the window, then the
menu bar.

`assert_text` reads every name and value of the window and of what is open on top of it. A
desktop app has no address, script or network of its own to control, so `navigate`,
`assert_url`, `eval_js`, `mock`, `a11y` and `visual` belong to the browser target only.

## The app's lifetime

A session launches the app and brings it forward; when the app is already running it joins that
one. At the end of the run it quits the app it launched and leaves alone one that was running
before. An app that refuses to quit, because it asks to save what the test typed, is terminated
so the next run starts clean.

State the app keeps between launches is yours to reset: the documents it reopens, its settings,
a session it offers to restore. Do it with `desktop.args`, or with `before_each` and
`after_each` [hooks](/docs/configuration).

## One at a time

A machine has one desktop, so runs on desktop targets take turns whatever `--parallel` is. A run
that waits for its turn is not timed while it waits. To run desktop tests in parallel, give each
its own machine, virtual machine or container, and split the suite with `--shard`.

## Artifacts and metrics

Each run directory has `screenshots/step-NN.png` (one per step, where the system allows
screenshots), `steps.json` (kind, text, success, reason, the agent's actions, timings) and
`agent-desktop.jsonl`, agent-desktop's own trace of what it read and did. `transcript.json` holds
the steps as a conversation, so `judge`, `contains` and `regex` assertions apply. Metrics:
`goal_steps` (agent actions over all goals), `desktop_actions`, `observations`, `cache_hits`,
`cache_misses`, `llm_cost_usd`, `llm_tokens`.

Observations, traces and screenshots hold whatever the app shows. They cover the app under test
only, never the rest of the desktop.

## Example

[`examples/desktop-editor`](https://github.com/xavidop/tamesu/tree/main/examples/desktop-editor)
writes a note in a text editor: TextEdit on macOS, or the editor you name in `DESKTOP_APP`. With
a model (`tamesu.yaml` there) the agent finds its own way; with the fake provider
(`tamesu.llmfake.yaml`) it replays scripted actions against the real app.
