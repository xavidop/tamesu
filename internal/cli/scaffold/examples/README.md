# Example suites

`support-agent.yaml` tests a REST agent and a LiveKit voice agent. Point the
`AGENT_URL` and `LIVEKIT_URL` variables at your agents (or edit the targets),
export the secrets the suite declares, then:

```sh
tamesu validate            # check tamesu.yaml and the suites
tamesu list                # list the tests
tamesu doctor              # resolve every secret and check the setup
```

Docs: https://xavidop.github.io/tamesu/docs/
