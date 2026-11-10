#!/bin/sh
# Tests scripts/release-meta.sh: tag parsing, RC selection, the pre-release digest record,
# the X.Y alias rule and the changelog extraction used by prerelease.yaml and release.yaml.
set -eu
script="$(cd "$(dirname "$0")" && pwd)/release-meta.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

# check <description> <expected output, or FAIL> <stdin> <args…>
check() {
	desc=$1 want=$2 input=$3
	shift 3
	if got=$(printf '%s' "$input" | "$script" "$@" 2>"$work/err"); then :; else got=FAIL; fi
	if [ "$got" = "$want" ]; then echo "ok   $desc"; else
		echo "FAIL $desc: expected [$want], got [$got] $(cat "$work/err")"
		failures=$((failures + 1))
	fi
}
nl='
'
D=sha256:$(printf '%064d' 0 | tr 0 a)

check "parse a stable tag" "version=0.1.0${nl}minor=0.1${nl}rc=${nl}kind=stable" "" parse v0.1.0
check "parse an RC tag" "version=1.12.3${nl}minor=1.12${nl}rc=10${nl}kind=rc" "" parse v1.12.3-rc.10
for bad in 0.1.0 v0.1 v01.1.0 v0.1.0-rc v0.1.0-rc.01 v0.1.0-beta.1 v0.1.0-rc.1x "v0.1.0 " v0.1.0+build; do
	check "parse refuses '$bad'" FAIL "" parse "$bad"
done

tags="v0.1.0-rc.1${nl}v0.1.0-rc.10${nl}v0.1.0-rc.2${nl}v0.1.0${nl}v0.1.1-rc.11${nl}v0x1x0-rc.99${nl}"
check "latest-rc sorts numerically" v0.1.0-rc.10 "$tags" latest-rc 0.1.0
check "latest-rc ignores other versions and dots as wildcards" FAIL "v0x1x0-rc.3${nl}" latest-rc 0.1.0
check "latest-rc fails without an RC" FAIL "v0.1.0${nl}" latest-rc 0.1.0
check "latest-rc refuses a bad version" FAIL "$tags" latest-rc 0.1

body="Release candidate.${nl}${nl}postelmd-image-digest: $D${nl}"
check "digest reads the record" "$D" "$body" digest
check "digest accepts CRLF (edited in the web UI)" "$D" "postelmd-image-digest: $D
" digest
check "digest fails without a record" FAIL "no digest here${nl}" digest
check "digest fails on two records" FAIL "$body$body" digest
check "digest fails on a short digest" FAIL "postelmd-image-digest: sha256:abc${nl}" digest
check "digest ignores an indented record" FAIL "  postelmd-image-digest: $D${nl}" digest
check "digest-line round-trips" "postelmd-image-digest: $D" "" digest-line "$D"
check "digest-line refuses a non-digest" FAIL "" digest-line latest

stable="v0.1.0${nl}v0.1.2${nl}v0.1.10${nl}v0.2.0${nl}v0.1.11-rc.1${nl}"
check "is-highest: a later patch moves X.Y" true "$stable" is-highest 0.1.11
check "is-highest: re-releasing the top patch moves X.Y" true "$stable" is-highest 0.1.10
check "is-highest: an older patch doesn't (numeric, not text)" false "$stable" is-highest 0.1.9
check "is-highest: the first release of a minor" true "$stable" is-highest 0.3.0
check "is-highest: RC tags don't count" true "v0.4.0-rc.1${nl}v0.4.1-rc.1${nl}" is-highest 0.4.0

cat >"$work/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

### Added
- not released yet

## [0.1.10] - 2026-12-01

### Fixed
- ten

## [0.1.1] - 2026-11-20

### Added
- first line

- second line

## [0.1.0] - 2026-11-15

## [0.0.9]
- no date

[Unreleased]: https://example.invalid
EOF
check "notes extract one section, trimmed" "### Added${nl}- first line${nl}${nl}- second line" "" \
	notes 0.1.1 "$work/CHANGELOG.md"
check "notes don't match 0.1.10 for 0.1.1 (or the reverse)" "### Fixed${nl}- ten" "" \
	notes 0.1.10 "$work/CHANGELOG.md"
check "notes fail on an empty section" FAIL "" notes 0.1.0 "$work/CHANGELOG.md"
check "notes fail on a section without a date" FAIL "" notes 0.0.9 "$work/CHANGELOG.md"
check "notes fail on a missing section" FAIL "" notes 0.2.0 "$work/CHANGELOG.md"
check "notes fail on a missing file" FAIL "" notes 0.1.1 "$work/none.md"
check "an unknown command fails" FAIL "" frobnicate

if [ "$failures" -ne 0 ]; then echo "$failures failure(s)"; exit 1; fi
echo "all release-meta tests passed"
