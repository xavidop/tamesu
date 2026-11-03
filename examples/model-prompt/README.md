# model-prompt: a system prompt under test, no server

The `model` target runs a system prompt and its tool definitions directly against a model. The
simulated user talks to the prompt, tamesu answers the prompt's tool calls from the results in
the suite, and the judge grades what the prompt said. This is the loop before an agent exists:
edit `prompts/support.md`, run, read the verdict.

```sh
# With a model (needs GEMINI_API_KEY):
tamesu run --config tamesu.yaml

# Without a model: the fake provider plays the prompt (a binary built with -tags llmfake):
tamesu run --config tamesu.llmfake.yaml
```

Once the prompt lives in an agent, point a `rest`, `grpc` or `livekit` target at it and keep
the same tests.
