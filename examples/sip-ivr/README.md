# sip-ivr: the phone example (Asterisk as carrier and IVR)

A phone agent under test with no carrier, number or trunk account: `examples/compose.yaml`
runs **livekit-server**, **Redis**, the **LiveKit SIP service** and **Asterisk**, which plays
both the carrier the SIP service trunks into and the IVR tamesu calls. The dialplan
(`asterisk/extensions.conf`) answers, plays a greeting, reads one digit, and on `2` sends a
DTMF `9` back, thanks the caller and hangs up.

`suite.yaml` has two tests:

- **outbound**: tamesu dials `+15550001` through the trunk `tamesu sip setup` created on the
  SIP service (address `asterisk:5060`), waits for "press 2 for billing" (`wait_for`), presses
  `2` (`dtmf`), waits for "billing department", then the persona speaks; it asserts
  `dtmf_received: 9`, `call_ended_by`, `call_setup_ms` and `greeting_ms`.
- **inbound**: the `trigger` command (with `{{sip.inbound_number}}` rendered) asks Asterisk to
  originate a call to tamesu's number `+15550002`; the SIP service's inbound trunk and dispatch rule (also from `tamesu sip setup`)
  drop it into the room `tamesu-sip-inbound`, where tamesu answers and navigates the same menu.

The speech models are the fake ones (`fake/tts` speaks a tone, `fake/stt` replays
`llmfake-script.json`), so no keys are needed: what the run proves is the plumbing, a real
SIP call set up through the LiveKit SIP API, audio in both directions, DTMF relayed both
ways, call status and hangup.

```sh
docker compose -f examples/compose.yaml up -d
export LIVEKIT_URL=ws://localhost:7880 LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret
tamesu sip setup --config examples/sip-ivr/tamesu.llmfake.yaml      # prints the trunk ids
export TAMESU_SIP_TRUNK=ST_...                                      # the outbound_trunk_id it printed
TAMESU_LLMFAKE_SCRIPT=examples/sip-ivr/llmfake-script.json tamesu run --config examples/sip-ivr/tamesu.llmfake.yaml
docker compose -f examples/compose.yaml down -v
```

`make integration-sip` does all of that (it needs Docker); the CI job `integration-sip` runs it
on every push. Against a real trunk, the same suite works with your own `sip:` block, see
[SIP setup](https://xavidop.github.io/tamesu/docs/sip-setup/).
