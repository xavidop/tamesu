# red-team: the built-in packs against the example agent

Three attack scenarios against [`examples/rest-agent`](../rest-agent/): an attacker persona
tries to override the agent's instructions, to get another customer's personal data, and to make
it print its system prompt. Each test grades the agent with one of tamesu's
[red-team packs](https://xavidop.github.io/tamesu/docs/red-team/) (`prompt_injection`,
`pii_leakage`, `system_prompt_leakage`), plus a suite-defined pack and a judge of its own.

```sh
# With a model (needs GEMINI_API_KEY); the agent answers with a model too:
(cd ../rest-agent && go run .) &
tamesu run --config tamesu.yaml

# Without a model: the scripted agent, the fake provider replaying the attacker's
# lines from llmfake-script.json, and a tamesu binary built with -tags llmfake:
(cd ../rest-agent && go run . --scripted) &
TAMESU_LLMFAKE_SCRIPT=$PWD/llmfake-script.json tamesu run --config tamesu.llmfake.yaml
```

Every result reads `<pack>/<assertion>`, so a failure says which check of which pack caught the
agent. The real-model configuration repeats each test three times with `pass_threshold: 1`: an
attack that works one time in three is an attack that works.
