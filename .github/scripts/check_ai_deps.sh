#!/usr/bin/env bash
# Fails if any module other than llm/ (and its llm/plugins/*) imports a model
# provider SDK or a Genkit model plugin directly, instead of going through the
# llm module. Every AI call in tamesu is a Genkit call made from llm/.
#
# Only the packages of each module are inspected (their direct imports), so
# cmd/tamesu may depend on llm, which depends on the SDKs, without failing.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
denylist='^(google\.golang\.org/genai|github\.com/openai/openai-go|github\.com/anthropics/anthropic-sdk-go|github\.com/deepgram/|github\.com/elevenlabs/|github\.com/firebase/genkit/go/plugins/(googlegenai|anthropic|compat_oai|ollama|vertexai|evaluators))'

status=0
while IFS= read -r modfile; do
	dir="$(dirname "$modfile")"
	case "$dir" in
	# llm is where models are called; the examples are standalone Genkit apps
	# (the agents under test), not tamesu modules.
	./llm | ./llm/plugins/* | ./examples/*) continue ;;
	esac
	offenders="$(cd "$dir" && GOWORK=off go list -f '{{$p := .ImportPath}}{{range .Imports}}{{$p}} {{.}}{{"\n"}}{{end}}' ./... 2>/dev/null | awk '{print $2 " <- " $1}' | grep -E "^($denylist)" || true)"
	if [ -n "$offenders" ]; then
		echo "check-ai-deps: $dir imports a model provider SDK directly; route it through the llm module:" >&2
		printf '  %s\n' "$offenders" >&2
		status=1
	fi
done < <(find . -name go.mod -not -path './site/*' -not -path '*/node_modules/*' -not -path '*/testdata/*' | sort)

if [ "$status" -eq 0 ]; then
	echo "check-ai-deps: only llm/ talks to model providers"
fi
exit "$status"
