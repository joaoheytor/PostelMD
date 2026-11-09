#!/bin/sh
# Runs every Go fuzz target for FUZZTIME (default 30s) each. CI runs it on every PR
# (docs/ci-cd.md, test-go); longer local runs: FUZZTIME=10m scripts/fuzz.sh
set -eu
cd "$(git rev-parse --show-toplevel)"
fuzztime=${FUZZTIME:-30s}

for pkg in $(go list ./...); do
  for target in $(go test -list '^Fuzz' "$pkg" | grep '^Fuzz' || true); do
    echo "== $pkg $target ($fuzztime)"
    go test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg"
  done
done
