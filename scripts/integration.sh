#!/usr/bin/env bash
# Runs the example suites end to end without network or keys: builds tamesu
# with -tags llmfake, starts the scripted rest-agent, runs suite.yaml (must
# pass) and broken.yaml against the --broken agent (must fail with the judge's
# reason), and leaves the JUnit reports under .tamesu/integration/.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off

out=.tamesu/integration
rm -rf "$out" && mkdir -p "$out" bin
echo "==> build tamesu (-tags llmfake)"
(cd cmd/tamesu && go build -tags llmfake -o ../../bin/tamesu-llmfake .)
echo "==> build rest-agent"
(cd examples/rest-agent && go build -o ../../bin/rest-agent .)

port="${AGENT_PORT:-18080}"
export AGENT_URL="http://127.0.0.1:$port/chat"

# wait_for polls the agent's port with bash's /dev/tcp (no curl needed).
wait_for() {
	for _ in $(seq 1 50); do
		if (echo >"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then return 0; fi
		sleep 0.2
	done
	echo "rest-agent did not start" >&2
	return 1
}

# stop_agent ends the agent and waits for it; a stubborn one is killed.
stop_agent() {
	kill "$1" 2>/dev/null || true
	for _ in $(seq 1 25); do
		kill -0 "$1" 2>/dev/null || break
		sleep 0.2
	done
	kill -9 "$1" 2>/dev/null || true
	wait "$1" 2>/dev/null || true
}

echo "==> scripted agent: the suite must pass"
./bin/rest-agent --scripted --addr "127.0.0.1:$port" &
agent=$!
trap 'stop_agent $agent' EXIT
wait_for
./bin/tamesu-llmfake run --config examples/rest-agent/tamesu.llmfake.yaml --out "$out/pass"
stop_agent "$agent"

echo "==> broken agent: the judge must fail it"
./bin/rest-agent --scripted --broken --addr "127.0.0.1:$port" &
agent=$!
wait_for
set +e
./bin/tamesu-llmfake run --config examples/rest-agent/tamesu.llmfake.yaml examples/rest-agent/broken.yaml --out "$out/fail"
code=$?
set -e
stop_agent "$agent"
if [ "$code" -ne 1 ]; then
	echo "expected exit code 1 for the broken agent, got $code" >&2
	exit 1
fi
grep -q "fake judge: the rubric asks for a failure" "$out/fail/junit.xml" || { echo "junit.xml lacks the judge reason" >&2; exit 1; }
echo "integration: ok (reports in $out)"
