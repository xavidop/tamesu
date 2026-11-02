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
export AGENT_WS_URL="ws://127.0.0.1:$port/ws"
grpc_port="${AGENT_GRPC_PORT:-18090}"
export AGENT_GRPC_URL="grpc://127.0.0.1:$grpc_port"

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

echo "==> scripted agent: the suite must pass (HTTP, websocket and gRPC)"
./bin/rest-agent --scripted --addr "127.0.0.1:$port" --grpc "127.0.0.1:$grpc_port" &
agent=$!
trap 'stop_agent $agent' EXIT
wait_for
./bin/tamesu-llmfake run --config examples/rest-agent/tamesu.llmfake.yaml --out "$out/pass"
stop_agent "$agent"

echo "==> red-team: the packs against the scripted agent must pass"
./bin/rest-agent --scripted --addr "127.0.0.1:$port" &
agent=$!
wait_for
TAMESU_LLMFAKE_SCRIPT="$PWD/examples/red-team/llmfake-script.json" ./bin/tamesu-llmfake run --config examples/red-team/tamesu.llmfake.yaml --out "$out/red-team"
grep -q "pii_leakage/not_regex" "$out/red-team/summary.json" || { echo "summary.json lacks the expanded pack assertions" >&2; exit 1; }
stop_agent "$agent"

echo "==> generate: a draft suite from the PRD must validate"
./bin/tamesu-llmfake generate --config examples/rest-agent/tamesu.llmfake.yaml --from examples/rest-agent/PRD.md --target api --out "$out/generated.yaml"
./bin/tamesu-llmfake validate --config examples/rest-agent/tamesu.llmfake.yaml "$out/generated.yaml"
./bin/tamesu-llmfake generate --config examples/rest-agent/tamesu.llmfake.yaml --from examples/rest-agent/PRD.md --target web --type browser --out "$out/generated-browser.yaml"
./bin/tamesu-llmfake validate --config examples/rest-agent/tamesu.llmfake.yaml "$out/generated-browser.yaml"
./bin/tamesu-llmfake generate --config examples/rest-agent/tamesu.llmfake.yaml --from examples/rest-agent/PRD.md --target rt --type realtime --out "$out/generated-realtime.yaml"
./bin/tamesu-llmfake validate --config examples/rest-agent/tamesu.llmfake.yaml "$out/generated-realtime.yaml"

echo "==> mcp: the server must list its tools over stdio"
printf '%s\n' \
	'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"integration","version":"0"}}}' \
	'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
	'{"jsonrpc":"2.0","id":2,"method":"tools/list"}' |
	./bin/tamesu-llmfake mcp --cwd examples/rest-agent >"$out/mcp.jsonl" 2>"$out/mcp.err"
grep -q '"tamesu_run"' "$out/mcp.jsonl" || { echo "tamesu mcp did not list tamesu_run:" >&2; cat "$out/mcp.jsonl" "$out/mcp.err" >&2; exit 1; }

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

# Browser: the demo web shop and the fake browser agent's scripted actions
# against the real Chromium (found locally, or downloaded by tamesu).
echo "==> browser: the shop suite must pass against the demo web app"
(cd examples/webapp && go build -o ../../bin/webapp .)
web_port="${WEBAPP_PORT:-18090}"
./bin/webapp --addr "127.0.0.1:$web_port" >"$out/webapp.log" 2>&1 &
web=$!
trap 'stop_agent $web' EXIT
for _ in $(seq 1 50); do
	if (echo >"/dev/tcp/127.0.0.1/$web_port") 2>/dev/null; then break; fi
	sleep 0.2
done
export TAMESU_CACHE_DIR="$PWD/$out/cache"
WEBAPP_URL="http://127.0.0.1:$web_port" TEST_PASSWORD=correct-horse TAMESU_LLMFAKE_SCRIPT="$PWD/examples/browser-shop/llmfake-script.json" \
	./bin/tamesu-llmfake run --config examples/browser-shop/tamesu.llmfake.yaml --out "$out/browser"
test -f "$out"/browser/tests/*/run-1/screenshots/step-01.png || { echo "no step screenshot was written" >&2; exit 1; }
test -f "$out"/browser/tests/*/run-1/trace.har || { echo "no HAR was written" >&2; exit 1; }
test -f "$out"/browser/report.html || { echo "no report.html was written" >&2; exit 1; }

echo "==> browser again with --replay: every goal must come from the action cache, no agent call"
WEBAPP_URL="http://127.0.0.1:$web_port" TEST_PASSWORD=correct-horse TAMESU_LLMFAKE_SCRIPT="$PWD/examples/browser-shop/llmfake-script.json" \
	./bin/tamesu-llmfake run --config examples/browser-shop/tamesu.llmfake.yaml --replay --out "$out/browser-replay"
python3 - "$out" <<'PY'
import json, glob, sys
out = sys.argv[1]
first = json.load(open(glob.glob(out + "/browser/tests/*/run-1/result.json")[0]))
second = json.load(open(glob.glob(out + "/browser-replay/tests/*/run-1/result.json")[0]))
m1, m2 = first["metrics"], second["metrics"]
assert m1.get("cache_misses") == 2 and m1.get("cache_hits") == 0, m1
assert m2.get("cache_hits") == 2 and m2.get("cache_misses") == 0, m2
assert second["passed"], second.get("assertions")
goal_calls = [s for s in json.load(open(glob.glob(out + "/browser-replay/tests/*/run-1/steps.json")[0])) if s["kind"] == "goal"]
assert all("replayed" in s.get("reason", "") for s in goal_calls), goal_calls
print("replay: %d goals replayed, %.2fs vs %.2fs" % (len(goal_calls), second["duration"] / 1e9, first["duration"] / 1e9))
PY
stop_agent "$web"

# LiveKit text mode: livekit-server --dev (the binary on the PATH, or the
# Docker image) with the example agent. Skipped, loudly, when neither exists.
lk_port="${LIVEKIT_PORT:-7880}"
lk_pid=""
lk_container=""
start_livekit() {
	if command -v livekit-server >/dev/null; then
		livekit-server --dev --port "$lk_port" >"$out/livekit-server.log" 2>&1 &
		lk_pid=$!
	elif command -v docker >/dev/null && docker info >/dev/null 2>&1; then
		lk_container=$(docker run -d --rm -p "$lk_port:7880" livekit/livekit-server --dev --bind 0.0.0.0)
	else
		return 1
	fi
	for _ in $(seq 1 100); do
		if (echo >"/dev/tcp/127.0.0.1/$lk_port") 2>/dev/null; then return 0; fi
		sleep 0.2
	done
	echo "livekit-server did not start" >&2
	return 1
}
stop_livekit() {
	[ -n "$lk_pid" ] && stop_agent "$lk_pid"
	[ -n "$lk_container" ] && docker stop "$lk_container" >/dev/null 2>&1 || true
}
if start_livekit; then
	echo "==> livekit text and audio mode: both suites must pass (fake speech models, real Opus tracks)"
	(cd examples/livekit-agent && go build -o ../../bin/livekit-agent .)
	export LIVEKIT_URL="ws://127.0.0.1:$lk_port" LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret
	./bin/livekit-agent --scripted --audio --url "$LIVEKIT_URL" >"$out/livekit-agent.log" 2>&1 &
	lk_agent=$!
	trap 'stop_agent $lk_agent; stop_livekit' EXIT
	sleep 1
	./bin/tamesu-llmfake run --config examples/livekit-agent/tamesu.llmfake.yaml --out "$out/livekit"
	stop_agent "$lk_agent"
	stop_livekit
else
	echo "==> livekit text and audio mode: SKIPPED (no livekit-server binary and no Docker daemon)"
fi
echo "integration: ok (reports in $out)"
