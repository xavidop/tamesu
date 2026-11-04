# a2a-agent

A tiny agent speaking the Agent2Agent protocol for the shoe shop: an agent card with two skills
(`order-status`, `returns`), `message/send`, `message/stream` over server-sent events, and a task
that asks for the order id (`input-required`) before it answers. The suite tests it with tamesu's
`a2a` target, once per request and once streamed.

```sh
go build -o ../../bin/a2a-agent . && ../../bin/a2a-agent --addr 127.0.0.1:8091
tamesu run --config tamesu.yaml                 # real models
tamesu run --config tamesu.llmfake.yaml         # a binary built with -tags llmfake, no network
```

The card is at `http://127.0.0.1:8091/.well-known/agent-card.json`.
