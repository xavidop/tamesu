#!/usr/bin/env bash
# Writes the bash, zsh, fish and PowerShell completion scripts to completions/,
# which GoReleaser ships in the release archives and installs from the Linux
# packages. Run from anywhere; it builds the CLI from cmd/tamesu.
#
# It builds with the workspace off and the module files read-only, as the
# release build does: GoReleaser refuses a dirty tree, and a `go run` inside
# the workspace rewrites go.work.sum.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -rf completions
mkdir completions
bin=$(mktemp -d)/tamesu
trap 'rm -rf "$(dirname "$bin")"' EXIT
(cd cmd/tamesu && GOWORK=off GOFLAGS=-mod=readonly CGO_ENABLED=0 go build -o "$bin" .)
for sh in bash zsh fish; do
	"$bin" completion "$sh" >"completions/tamesu.$sh"
done
"$bin" completion powershell >"completions/tamesu.ps1"
