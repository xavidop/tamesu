# rest-agent: the example REST agent

A tiny customer-support agent for an online shoe store, built with
[Genkit](https://genkit.dev), that tamesu's example suite tests over HTTP and over a websocket.
It answers `POST /chat {session_id, message}` with `{reply, tool_calls, end_session, session_id}`,
and the same conversation on `GET /ws`, where each reply is streamed as `{delta}` messages and
closed by `{done: true, end_session, tool_calls}` (the suite's `ws` target, `streaming: websocket`).

```sh
# With a model (needs GEMINI_API_KEY):
go run . &
tamesu run --config tamesu.yaml

# Without a model: the agent answers from rules and tamesu uses the fake provider
# (a binary built with -tags llmfake):
go run . --scripted &
tamesu run --config tamesu.llmfake.yaml

# A broken agent that promises return exceptions; the judge must fail it:
go run . --scripted --broken &
tamesu run --config tamesu.llmfake.yaml broken.yaml
```

`--grpc :9090` also serves tamesu's gRPC turn API (the `agent_grpc` target of the suite,
`AGENT_GRPC_URL`). `PRD.md` is the product note the agent implements; `tamesu generate --from PRD.md` drafts a
suite from it.

```sh
# Conversations the agent already had, replayed as tests (no simulator needed):
tamesu import conversations.json --target api --out recorded.yaml --golden-dir golden
tamesu run --config tamesu.llmfake.yaml recorded.yaml

# How far the judge agrees with people on each rubric:
tamesu judge calibrate labels.yaml --config tamesu.llmfake.yaml
```

`conversations.json` holds two recorded conversations in the JSON shape `tamesu import` reads;
`labels.yaml` holds transcripts a person marked pass or fail for `tamesu judge calibrate`.
