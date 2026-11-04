# mcp-server

A tiny MCP server for the shoe shop (`lookup_policy`, `lookup_order`, `end_session`), served over
stdio by default or over streamable HTTP with `--http 127.0.0.1:8090`, and the suite that tests it
with tamesu's `mcp` target: a model drives the server's tools and every call runs on the server.

```sh
go build -o ../../bin/mcp-server .
tamesu run --config tamesu.yaml                 # real models
tamesu run --config tamesu.llmfake.yaml         # a binary built with -tags llmfake, no network
```
