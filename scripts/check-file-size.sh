#!/bin/sh
# File-size limits for tracked source files (CLAUDE.md rule 2): warn above 300 lines,
# fail above 500. Generated files, fixtures and lockfiles are excluded.
set -eu
cd "$(git rev-parse --show-toplevel)"

soft=300
hard=500
fail=0

git ls-files -z -- '*.go' '*.ts' '*.js' '*.mjs' '*.svelte' '*.py' '*.sh' |
  tr '\0' '\n' |
  grep -vE '(^|/)(testdata|fixtures|generated|node_modules|dist)/|\.gen\.|_gen\.go$|\.pb\.go$' |
  {
    while IFS= read -r f; do
      [ -f "$f" ] || continue
      n=$(awk 'END { print NR }' "$f") # also counts a last line without a newline
      if [ "$n" -gt "$hard" ]; then
        echo "error: $f has $n lines (hard limit $hard)" >&2
        fail=1
      elif [ "$n" -gt "$soft" ]; then
        echo "warning: $f has $n lines (soft limit $soft; split it soon)" >&2
      fi
    done
    exit "$fail"
  }
