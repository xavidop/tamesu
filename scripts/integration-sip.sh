#!/usr/bin/env bash
# Runs the sip example end to end with Docker: brings up examples/compose.yaml
# (livekit-server, Redis, livekit/sip, Asterisk), runs `tamesu sip setup`
# against it, then the suite with the fake provider and fake speech models.
# Needs Docker with compose; a tamesu binary with libopus (not the lite build).
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off

compose="docker compose -f examples/compose.yaml"
out=.tamesu/integration-sip
rm -rf "$out" && mkdir -p "$out" bin

if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then
	echo "integration-sip: SKIPPED (no Docker daemon)"
	exit 0
fi

echo "==> build tamesu (-tags llmfake)"
(cd cmd/tamesu && go build -tags llmfake -o ../../bin/tamesu-llmfake .)

echo "==> compose up"
$compose up -d --quiet-pull
trap '$compose logs --no-color >"$out/compose.log" 2>&1 || true; $compose down -v >/dev/null 2>&1 || true' EXIT

# Wait for livekit-server's HTTP port and give the SIP service a moment to register.
for _ in $(seq 1 100); do
	if (echo >"/dev/tcp/127.0.0.1/7880") 2>/dev/null; then break; fi
	sleep 0.5
done
sleep 5

export LIVEKIT_URL="ws://127.0.0.1:7880" LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret
export TAMESU_LLMFAKE_SCRIPT="$PWD/examples/sip-ivr/llmfake-script.json"

echo "==> tamesu sip setup: trunks and dispatch rule on the SIP service"
./bin/tamesu-llmfake sip setup --config examples/sip-ivr/tamesu.llmfake.yaml | tee "$out/setup.txt"
TAMESU_SIP_TRUNK=$(sed -n 's/^ *outbound_trunk_id: *//p' "$out/setup.txt" | head -1)
[ -n "$TAMESU_SIP_TRUNK" ] || { echo "sip setup printed no outbound_trunk_id" >&2; exit 1; }
export TAMESU_SIP_TRUNK

echo "==> second setup run must reuse everything"
./bin/tamesu-llmfake sip setup --config examples/sip-ivr/tamesu.llmfake.yaml | tee "$out/setup2.txt"
if grep -q '^created' "$out/setup2.txt"; then echo "sip setup is not idempotent" >&2; exit 1; fi

echo "==> doctor"
./bin/tamesu-llmfake doctor --config examples/sip-ivr/tamesu.llmfake.yaml | tee "$out/doctor.txt" || true

echo "==> the IVR suite must pass (outbound and inbound calls through Asterisk)"
./bin/tamesu-llmfake run --config examples/sip-ivr/tamesu.llmfake.yaml --out "$out/run"

echo "==> a number outside allowed_numbers is refused with exit 2"
set +e
./bin/tamesu-llmfake run --config examples/sip-ivr/tamesu.llmfake.yaml --var agent_number=+34910000000 examples/sip-ivr/disallowed.yaml --out "$out/refused"
code=$?
set -e
if [ "$code" -ne 2 ]; then echo "expected exit code 2 for a disallowed number, got $code" >&2; exit 1; fi
echo "integration-sip: ok (reports in $out)"
