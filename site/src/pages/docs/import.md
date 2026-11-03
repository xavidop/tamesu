---
layout: ../../layouts/DocsLayout.astro
title: Importing conversations
---

# Importing conversations

![A stack of transcripts sliding into a funnel that outputs one tidy document](/img/import.jpg)

`tamesu import` turns conversations your agent already had with real people into tests. Each
conversation becomes a test that says the user's lines, in order, and compares the agent's
answers with the ones it gave back then. Run it after a prompt change, a model upgrade or a
refactor, and the suite tells you which recorded conversations the agent still handles the same
way.

```sh
tamesu import exports/chats.json --target api --out tests/recorded.yaml --golden-dir golden
tamesu import support.csv --target voice --type livekit --out tests/calls.yaml
tamesu import chats.jsonl --target api            # prints the suite with the answers inline
```

## Input formats

The format follows the file's extension.

| File | Shape |
|---|---|
| `.json` | An array of conversations, each with an `id` (or `name`, `conversation_id`) and `turns` (or `messages`) of `{role, text}`; or a flat array of messages carrying a `conversation_id`; or an object with a `conversations` key. A tamesu `transcript.json` works too. |
| `.jsonl`, `.ndjson` | One conversation or one message per line, with the same keys. |
| `.csv` | A header row with a conversation column (`conversation_id`, `conversation`, `session_id`, `id`), a `role` column and a `text` column (`content` and `message` also work). Rows stay in file order. |

Roles are normalised: `user`, `human`, `customer` and `caller` are the user; `agent`,
`assistant`, `bot` and `ai` are the agent. Lines with other roles, such as `system` or `tool`, are
left out. Consecutive lines by the same side are merged into one turn. An opening agent greeting
before the first user line is not part of the reference, since a text agent only answers.
Conversations without a user line followed by an agent answer are skipped and listed.

### Exports from LangSmith, Langfuse and Voiceflow

The exports of the usual observability tools are read as they come, so a conversation that
happened in production is a test in one command. `--format` names the export; without it the
file's keys tell them apart.

| Format | What tamesu reads |
|---|---|
| `langsmith` | A runs export (JSON or JSON Lines). Each run's `inputs` carry the conversation so far (`messages`, or a single `input`) and its `outputs` the answer (`output`, `messages`, `generations`). Runs sharing a thread (`thread_id`, `session_id` or `conversation_id` in the metadata) are one conversation, taken from the run with the longest history; a run without one is a conversation of its own. LangChain's serialised messages (`HumanMessage`, `AIMessage`) are understood. |
| `langfuse` | A traces export, or a page of the traces API (`data: [...]`). A trace's `input` is the conversation so far (a messages list or a prompt) and its `output` the answer; when the trace has neither, the last `GENERATION` observation serves. Traces sharing a `sessionId` are one conversation. |
| `voiceflow` | A transcript's dialog turns (the Dialog Manager transcript API): `request` turns are what the user said (`payload.payload.query`, or the intent's label), `text` and `speak` turns what the agent answered (`payload.message`, or the Slate rich text). A list of transcripts that each carry `turns` imports them all. |

```bash
tamesu import runs.jsonl --format langsmith --target api --out tests/from-langsmith.yaml --golden-dir golden
```

Whatever the source, system and tool messages are dropped, consecutive lines of one side are
merged, and an opening greeting the user never prompted is left out, so the script starts with
the user.

## What you get

```yaml
# Imported by tamesu import from chats.json: 2 conversation(s).
suite: chats
targets:
  api:
    type: rest
    url: "${AGENT_URL}"
    method: POST
    request: { body: { session_id: "{{session}}", message: "{{message}}" } }
    response: { text: "$.reply", tool_calls: "$.tool_calls[*].name", done: "$.end_session" }
tests:
  - name: refund-2026-10-30
    target: api
    tags: [imported]
    scenario:
      script:
        - say: "Where is my refund for order 1234?"
        - say: "Thanks"
    assert:
      - golden: { file: golden/refund-2026-10-30.json, mode: similar }
```

The target is a stub: point `url` and the request and response mapping at your agent, as in any
suite. A scenario with a `script` and no `goal` plays the script and stops, so these tests need no
simulator model; with `similar` or `exact` golden mode they need no judge either, and run with
no model key at all.

`--golden-dir` writes each conversation's agent answers to `<dir>/<test>.json` next to the
suite, the same files `tamesu run --update-golden` maintains; without it the answers are inline
as `turns`. `--golden-mode` picks how answers are compared: `similar` (word overlap of at least
80%, the default; set `min` in the suite to tune it), `exact` (normalised text) or `judge` (the
judge decides whether each answer means the same). `--tags` sets the tags on every test
(`imported` by default) so the recorded suite can be selected or excluded with `--tags`.

## Keeping the suite honest

The recorded answers are what the agent said, not necessarily what it should have said. Read the
suite once: delete conversations that went badly, or turn their golden assertion into a `judge`
rubric that states the right behaviour. When the agent's answers change on purpose, run
`tamesu run --update-golden` on the tests you reviewed and commit the new reference files.

The suite validates as it is written (`tamesu validate` runs on the output). Exit `0` when it
validates, `1` when it does not (the files are kept), `2` on a usage or config error. See
[Reference answers](/docs/judges#reference-answers) for the golden assertion and
[Generating tests](/docs/generate/) for drafting tests from documents instead.
