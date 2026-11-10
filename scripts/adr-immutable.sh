#!/bin/sh
# An Accepted ADR's "## Decision" section is immutable (docs/adr/README.md): fails when a
# change edits it, or deletes or renames an Accepted ADR.
#
# usage: adr-immutable.sh <diff-base> <new> <ref>...
#   <diff-base>  the commit the change is diffed against (HEAD in pre-commit, the PR base in CI)
#   <new>        --index for the staged content (pre-commit), or a commit (CI)
#   <ref>...     where "Accepted" is judged: an ADR Accepted in ANY of them is immutable
#                (pre-commit: the merge base with origin/main and origin/main's tip)
# Renames are detected (-M), so `git mv` + edit is compared with the old path.
set -eu
g() { git -c color.ui=never -c core.fsmonitor=false "$@"; }
[ $# -ge 3 ] || { echo "usage: $0 <diff-base> <new>|--index <ref>..." >&2; exit 2; }
from=$1 new=$2
shift 2

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
decision() { awk '/^## / { if (d) exit; d = ($0 == "## Decision"); next } d' "$1"; }

if [ "$new" = --index ]; then
  g diff --cached --name-status -M "$from" -- docs/adr > "$tmp/changes"
  show_new() { g show ":$1"; }
else
  g diff --name-status -M "$from" "$new" -- docs/adr > "$tmp/changes"
  show_new() { g show "$new:$1"; }
fi

fail=0
# One line per change: <status> <path> [<new path>], tab-separated.
while IFS="$(printf '\t')" read -r status old path; do
  path=${path:-$old}
  case "$old" in docs/adr/[0-9][0-9][0-9][0-9]-*.md) ;; *) continue ;; esac
  for ref in "$@"; do
    g show "$ref:$old" > "$tmp/ref" 2>/dev/null || continue
    grep -q '^- \*\*Status:\*\* Accepted' "$tmp/ref" || continue
    case "$status" in
      D)
        echo "adr: $old is Accepted ($ref) and the change deletes it. Supersede it instead." >&2
        fail=1; break ;;
    esac
    show_new "$path" > "$tmp/new"
    decision "$tmp/ref" > "$tmp/decision-ref"
    decision "$tmp/new" > "$tmp/decision-new"
    if ! cmp -s "$tmp/decision-ref" "$tmp/decision-new"; then
      [ "$path" = "$old" ] && where="" || where=" (renamed to $path)"
      echo "adr: $old is Accepted ($ref) and its Decision section changed$where." >&2
      echo "  Supersede that part with a new ADR, or append a dated entry under" >&2
      echo "  '## Clarifications' (rationale, links, typos only; docs/adr/README.md)." >&2
      fail=1
    fi
    break
  done
done < "$tmp/changes"
exit "$fail"
