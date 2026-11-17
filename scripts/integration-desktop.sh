#!/usr/bin/env bash
# The desktop target end to end, with the real agent-desktop CLI (the pinned
# version, downloaded and checked) driving a real GTK editor (mousepad) in a
# headless Linux desktop session:
#
#   1. the target's integration test (targets/desktop, -tags integration);
#   2. the desktop example through the tamesu binary, with the fake agent
#      replaying examples/desktop-editor/llmfake-script.json, then again from
#      the action cache with --replay.
#
# On Linux with the packages installed it runs in place; anywhere else, or
# with DESKTOP_DOCKER=1 (what CI does), it runs in a container of a pinned
# system.
#
#   scripts/integration-desktop.sh
#
# Linux packages: xvfb openbox dbus x11-utils at-spi2-core libatk-bridge2.0-0 mousepad
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

if [ "${1:-}" = "--inside" ]; then
	# Inside the desktop session.
	work="${DESKTOP_WORK:-$(mktemp -d)}"
	# The editor keeps its settings and its state here, not in the home
	# directory, and does not offer to restore the session of a run that was
	# closed with unsaved text.
	export XDG_CONFIG_HOME="$work/config" XDG_DATA_HOME="$work/data" GSETTINGS_BACKEND=keyfile
	mkdir -p "$XDG_CONFIG_HOME/glib-2.0/settings" "$XDG_DATA_HOME"
	printf "[org/xfce/mousepad/preferences/file]\nsession-restore='never'\n" >"$XDG_CONFIG_HOME/glib-2.0/settings/keyfile"
	echo "==> desktop: the target's integration test"
	(cd targets/desktop && TAMESU_TEST_DOWNLOAD=1 GOWORK=off go test -tags integration -count=1 -run Integration ./...)

	echo "==> desktop: build tamesu (-tags llmfake,lite)"
	(cd cmd/tamesu && CGO_ENABLED=0 GOWORK=off go build -tags "llmfake lite" -o "$work/tamesu-llmfake" .)

	export TAMESU_CACHE_DIR="$work/cache" DESKTOP_APP=mousepad NOTE_PIN=4821
	export TAMESU_LLMFAKE_SCRIPT="$PWD/examples/desktop-editor/llmfake-script.json"

	echo "==> desktop: tamesu doctor reports agent-desktop and its permissions"
	"$work/tamesu-llmfake" doctor --config examples/desktop-editor/tamesu.llmfake.yaml >"$work/doctor.txt" 2>&1 || { cat "$work/doctor.txt" >&2; exit 1; }
	grep -E "agent-desktop|desktop permissions" "$work/doctor.txt"
	grep -q "ok   agent-desktop" "$work/doctor.txt" || { echo "doctor did not report agent-desktop" >&2; exit 1; }
	grep -q "ok   desktop permissions" "$work/doctor.txt" || { echo "doctor did not report the desktop permissions" >&2; exit 1; }

	echo "==> desktop: the editor suite must pass against a real editor"
	"$work/tamesu-llmfake" run --config examples/desktop-editor/tamesu.llmfake.yaml --out "$work/run"
	test -f "$work"/run/tests/*/run-1/steps.json || { echo "no steps.json was written" >&2; exit 1; }
	test -f "$work"/run/tests/*/run-1/agent-desktop.jsonl || { echo "no agent-desktop trace was written" >&2; exit 1; }
	if grep -rq 4821 "$work/run" --include='*.json' --include='*.html' --include='*.xml'; then
		echo "the secret is in the reports" >&2
		exit 1
	fi

	echo "==> desktop again with --replay: the goal must come from the action cache"
	"$work/tamesu-llmfake" run --config examples/desktop-editor/tamesu.llmfake.yaml --replay --out "$work/replay"
	grep -q '"cache_hits": 1' "$work"/replay/tests/*/run-1/result.json || { echo "the replay did not hit the action cache" >&2; exit 1; }
	echo "==> desktop: ok"
	exit 0
fi

if [ "$(uname -s)" = "Linux" ] && [ "${DESKTOP_DOCKER:-}" != "1" ]; then
	cd "$root"
	exec scripts/desktop-session.sh scripts/integration-desktop.sh --inside
fi

go_version="$(sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' "$root/go.mod")"
# The system the container runs. It is pinned, here and in CI, so the editor
# and its toolkit are the ones this was measured against: on Ubuntu 26.04 a
# key press by agent-desktop (1.2.0 to 1.3.1) crashes the GTK 3 editors tried,
# which is neither tamesu's nor the editor's to fix. DESKTOP_BASE names
# another system to try.
base="${DESKTOP_BASE:-debian:bookworm}"
image="tamesu-desktop-integration:go$go_version-$(echo "$base" | tr -c 'a-zA-Z0-9.\n' '-')"
docker build -q -t "$image" - >/dev/null <<DOCKERFILE
FROM golang:$go_version AS go
FROM $base
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:\$PATH GOPATH=/go
RUN apt-get update \\
 && apt-get install -y --no-install-recommends ca-certificates xvfb openbox dbus x11-utils at-spi2-core libatk-bridge2.0-0 mousepad \\
 && rm -rf /var/lib/apt/lists/*
DOCKERFILE
exec docker run --rm \
	-v "$root":/src:ro \
	-v tamesu-desktop-gomod:/go/pkg/mod \
	-v tamesu-desktop-gocache:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false \
	-w /src "$image" \
	scripts/desktop-session.sh scripts/integration-desktop.sh --inside
