#!/usr/bin/env bash
# Fails if the module in <moddir> (default: .) depends, directly or
# transitively, on a heavy SDK that belongs in a leaf module: Genkit and the
# model SDKs (llm/), LiveKit and pion (targets/livekit, targets/sip), chromedp
# (targets/browser), CGO audio (audio/), or a cloud SDK (the mamori providers
# are linked by cmd/tamesu only).
set -euo pipefail

moddir="${1:-.}"
denylist='^(github\.com/firebase/genkit|google\.golang\.org/genai|github\.com/openai/openai-go|github\.com/anthropics/anthropic-sdk-go|github\.com/livekit/|github\.com/pion/|github\.com/chromedp/|github\.com/yalue/onnxruntime_go|gopkg\.in/hraban/opus|github\.com/aws/aws-sdk-go|cloud\.google\.com/go|github\.com/Azure/azure-sdk-for-go|k8s\.io/|github\.com/hashicorp/vault|github\.com/xavidop/mamori/providers/)'

mods="$(cd "$moddir" && GOWORK=off go list -m all)"
offenders="$(printf '%s\n' "$mods" | awk '{print $1}' | grep -E "$denylist" || true)"

if [ -n "$offenders" ]; then
	echo "check-root-deps: $moddir must not depend on Genkit, LiveKit, pion, chromedp, CGO audio or cloud SDKs; found:" >&2
	printf '  %s\n' $offenders >&2
	exit 1
fi
echo "check-root-deps: $moddir is free of heavy SDKs"
