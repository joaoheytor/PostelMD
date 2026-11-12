#!/bin/sh
# modernc.org/sqlite needs modernc.org/libc at exactly the version its own go.mod requires
# (ADR-0015, clarification of 2026-11-12). Fails when the build selects another libc, for
# example after a bump of some other module raised it.
set -eu
cd "$(git rev-parse --show-toplevel)"

sqlite=$(go list -m -f '{{.Version}}' modernc.org/sqlite)
selected=$(go list -m -f '{{.Version}}' modernc.org/libc)
required=$(go mod graph | awk -v from="modernc.org/sqlite@$sqlite" \
  '$1 == from && index($2, "modernc.org/libc@") == 1 { sub(/^modernc\.org\/libc@/, "", $2); print $2 }')

if [ -z "$required" ]; then
  echo "error: go mod graph shows no modernc.org/libc requirement for modernc.org/sqlite@$sqlite" >&2
  exit 1
fi
if [ "$selected" != "$required" ]; then
  echo "error: modernc.org/libc is $selected, but modernc.org/sqlite@$sqlite requires exactly $required;" >&2
  echo "       run: go get modernc.org/libc@$required" >&2
  exit 1
fi
