#!/bin/sh
# The text logic of prerelease.yaml and release.yaml (docs/ci-cd.md §4 and §5), kept here so
# it is tested (scripts/release-meta_test.sh) rather than buried in workflow YAML.
#
#   release-meta.sh parse <tag>          vX.Y.Z or vX.Y.Z-rc.N → version=, minor=, rc=, kind=
#   release-meta.sh latest-rc <X.Y.Z>    tag names on stdin → the highest vX.Y.Z-rc.N
#   release-meta.sh digest               a pre-release body on stdin → its index digest
#   release-meta.sh is-highest <X.Y.Z>   tag names on stdin → "true" if no stable vX.Y.* is
#                                        higher (the X.Y alias may move), else "false"
#   release-meta.sh notes <X.Y.Z> <file> CHANGELOG.md → the body of `## [X.Y.Z] - YYYY-MM-DD`
#   release-meta.sh digest-line <digest> the line `digest` reads, for prerelease.yaml
#   release-meta.sh record <repo> <tag> the body of the release for <tag> (drafts included);
#                                        exit 3 if there is none (needs gh, GH_TOKEN)
#
# Every command fails (exit 1, message on stderr) rather than guess.
set -eu

fail() { echo "release-meta: $*" >&2; exit 1; }
num='(0|[1-9][0-9]*)'
semver="$num\\.$num\\.$num"
digest_re='sha256:[0-9a-f]{64}'
marker='postelmd-image-digest:'

is_version() { printf '%s\n' "$1" | grep -Eqx "$semver"; }

cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
parse)
	tag=${1:?usage: parse <tag>}
	printf '%s\n' "$tag" | grep -Eqx "v$semver(-rc\\.$num)?" ||
		fail "$tag: not a release tag name (vX.Y.Z or vX.Y.Z-rc.N)"
	rest=${tag#v}
	version=${rest%%-rc.*}
	rc=
	[ "$rest" = "$version" ] || rc=${rest#*-rc.}
	echo "version=$version"
	echo "minor=${version%.*}"
	echo "rc=$rc"
	if [ -n "$rc" ]; then echo kind=rc; else echo kind=stable; fi
	;;
latest-rc)
	version=${1:?usage: latest-rc <X.Y.Z>}
	is_version "$version" || fail "$version: not X.Y.Z"
	esc=$(printf '%s' "$version" | sed 's/\./\\./g')
	best=$(grep -Ex "v$esc-rc\\.$num" | sed 's/.*-rc\.//' | sort -n | tail -n 1) || true
	[ -n "$best" ] || fail "no tag v$version-rc.N among the given tags"
	echo "v$version-rc.$best"
	;;
digest)
	lines=$(grep -E "^$marker " | tr -d '\r') || true
	count=$(printf '%s' "$lines" | grep -c '' || true)
	[ "$count" = 1 ] || fail "expected one '$marker' line, found $count"
	d=${lines#"$marker "}
	printf '%s\n' "$d" | grep -Eqx "$digest_re" || fail "not a digest: $d"
	echo "$d"
	;;
record)
	# The body of the GitHub release (draft or not) for <tag>: exit 0 and the body, exit 3 if
	# none exists, exit 1 on any error. Lists releases rather than GET …/releases/tags/<tag>,
	# which hides drafts and makes "not found" and an outage hard to tell apart: here a
	# failed call is a failure, and only a complete listing without the tag means "none".
	repo=${1:?usage: record <owner/repo> <tag>}
	tag=${2:?usage: record <owner/repo> <tag>}
	printf '%s\n' "$tag" | grep -Eqx "v$semver(-rc\\.$num)?" || fail "$tag: not a release tag name"
	out=$(gh api --paginate "repos/$repo/releases?per_page=100" \
		--jq ".[] | select(.tag_name == \"$tag\") | {body: (.body // \"\")}") ||
		fail "listing the releases of $repo failed"
	[ -n "$out" ] || exit 3
	n=$(printf '%s\n' "$out" | jq -s length) || fail "unreadable release listing"
	[ "$n" = 1 ] || fail "$n releases for $tag"
	printf '%s\n' "$out" | jq -r .body
	;;
digest-line)
	d=${1:?usage: digest-line <digest>}
	printf '%s\n' "$d" | grep -Eqx "$digest_re" || fail "not a digest: $d"
	echo "$marker $d"
	;;
is-highest)
	version=${1:?usage: is-highest <X.Y.Z>}
	is_version "$version" || fail "$version: not X.Y.Z"
	minor=${version%.*}
	patch=${version##*.}
	esc=$(printf '%s' "$minor" | sed 's/\./\\./g')
	top=$(grep -Ex "v$esc\\.$num" | sed 's/.*\.//' | sort -n | tail -n 1) || true
	if [ -z "$top" ] || [ "$patch" -ge "$top" ]; then echo true; else echo false; fi
	;;
notes)
	version=${1:?usage: notes <X.Y.Z> <changelog>}
	file=${2:?usage: notes <X.Y.Z> <changelog>}
	is_version "$version" || fail "$version: not X.Y.Z"
	[ -f "$file" ] || fail "$file: no such file"
	esc=$(printf '%s' "$version" | sed 's/\./\\./g')
	grep -Eq "^## \\[$esc\\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$" "$file" ||
		fail "$file has no '## [$version] - YYYY-MM-DD' section"
	body=$(awk -v h="## [$version] - " '
		index($0, h) == 1 { on = 1; next }
		on && /^## \[/ { exit }
		on { print }' "$file")
	printf '%s\n' "$body" | grep -q '[^[:space:]]' || fail "the [$version] section is empty"
	# Trim leading and trailing blank lines.
	printf '%s\n' "$body" | awk 'NF { for (; n > 0; n--) print ""; started = 1; print; next }
		started { n++ }'
	;;
*)
	fail "usage: release-meta.sh parse|latest-rc|digest|digest-line|record|is-highest|notes …"
	;;
esac
