---
layout: ../../layouts/DocsLayout.astro
title: SIP setup
---

# SIP setup

The [sip target](/docs/targets/sip/) needs two things you bring: a **LiveKit server with the SIP
service** and a **SIP trunk** with a number. tamesu does not run a carrier of its own.

## The LiveKit SIP service

- **LiveKit Cloud** includes SIP; use your project's `wss://` URL and API key and secret.
- **Self-hosted**: run `livekit-server` with Redis and the
  [`livekit/sip`](https://github.com/livekit/sip) service pointed at the same Redis and API key.
  `examples/compose.yaml` is a complete local setup (with Asterisk standing in for the carrier)
  that you can adapt.

Put the URL in `sip.livekit_url` and the keys in `secrets:` as `LIVEKIT_API_KEY` and
`LIVEKIT_API_SECRET`.

## The trunk

Any SIP trunk works: Twilio Elastic SIP Trunking, Telnyx, Plivo, or your own PBX (Asterisk,
FreeSWITCH). Describe it once in `tamesu.yaml`:

```yaml
sip:
  livekit_url: ${LIVEKIT_URL}
  inbound_number: "+34910000000"             # a number of the trunk routed to tamesu (inbound tests)
  allowed_numbers: ["+1800555*", "+34*"]     # what tamesu may dial
  trunk:
    address: sip.twilio.com                  # the provider's SIP host (host:port for a PBX)
    numbers: ["+34910000000"]                # the trunk's numbers: outbound caller IDs, inbound numbers
    auth_username: "{{secrets.SIP_USER}}"    # outbound authentication
    auth_password: "{{secrets.SIP_PASSWORD}}"
    transport: auto                          # auto | udp | tcp | tls
    allowed_addresses: ["54.172.60.0/23"]    # who may call the inbound trunk (the provider's IPs); optional
secrets:
  LIVEKIT_API_KEY: env:LIVEKIT_API_KEY
  LIVEKIT_API_SECRET: env:LIVEKIT_API_SECRET
  SIP_USER: env:SIP_USER
  SIP_PASSWORD: op://CI/twilio-trunk/password
```

Then:

```sh
tamesu sip setup
```

creates, or reuses by name, the outbound trunk `tamesu-outbound`, the inbound trunk
`tamesu-inbound` and the dispatch rule `tamesu-inbound` (which routes calls to the trunk's
numbers into the room `sip.inbound_room`, `tamesu-sip-inbound` by default), and prints:

```yaml
sip:
  outbound_trunk_id: ST_abc123
  inbound_trunk_id: ST_def456
  dispatch_rule_id: SDR_789xyz
```

Paste the ids into `tamesu.yaml` (only `outbound_trunk_id` is read at run time; the others
document what exists). `tamesu sip setup --outbound` or `--inbound` sets up one side.

On the provider's side, point the trunk's **origination** (inbound) at your LiveKit SIP service
URI (`sip:<your-sip-host>:5060`, or the Cloud project's SIP URI) so calls to
`inbound_number` reach tamesu, and allow **termination** (outbound) from it with the username
and password above.

## Checking

`tamesu doctor` reports `sip`: `livekit_url`, the trunk id, `inbound_number` and the keys for
the sip targets your suites declare, and `livekit` probes `sip.livekit_url` over HTTP. A first
outbound test against your own phone number is the end-to-end check: you should hear the
persona speak.

## Cost and safety

Real calls cost real money. `allowed_numbers` is the fence: a `to` that comes from a variable or
secret is refused unless it matches a pattern, and `tamesu run` exits `2` before dialing. Keep
`sip.max_concurrent_calls` low (the default is 2) and `max_call_duration` short; the SIP
service ends calls that run past it.
