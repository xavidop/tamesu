#!/usr/bin/env bash
# Prepares the directory demo.tape records in: a fresh dir with the tamesu
# binary copied into it, so the tape shows `tamesu init`, `validate`, `list`
# and `doctor` from an empty project.
#
#   scripts/demo-seed.sh <path-to-tamesu-binary> <demo-dir>
set -euo pipefail
bin="${1:?tamesu binary}"
dir="${2:?demo directory}"
rm -rf "$dir"
mkdir -p "$dir/bin"
cp "$bin" "$dir/bin/tamesu"
echo "demo: $dir ready"
