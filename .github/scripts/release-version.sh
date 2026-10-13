#!/usr/bin/env bash
# Picks the version a push to main releases and writes it into
# CHANGELOG.md, then prints it.
#
# - A top entry "## [X.Y.Z] - Unreleased" is released as X.Y.Z (or the next
#   patch no tag has taken, when vX.Y.Z is already a tag): its heading gets
#   today's date and its notes stay as written.
# - A top entry already dated has been released, so the next patch no tag has
#   taken is released, with a dated entry of its own above it noting a
#   maintenance release.
#
#   release-version.sh [DATE]
set -euo pipefail
today="${1:-$(date -u +%Y-%m-%d)}"
file=CHANGELOG.md
top="$(grep -m1 -E '^## \[' "$file")"
version="$(sed -nE 's/^## \[([0-9]+\.[0-9]+\.[0-9]+)\].*/\1/p' <<<"$top")"
if [ -z "$version" ]; then
  echo "the CHANGELOG's top entry is not a version: $top" >&2
  exit 1
fi
next_free() {
  local v="$1"
  while git rev-parse -q --verify "refs/tags/v$v" >/dev/null; do
    v="${v%.*}.$(( ${v##*.} + 1 ))"
  done
  printf '%s' "$v"
}
if [[ "$top" == *" - Unreleased" ]]; then
  new="$(next_free "$version")"
  awk -v top="$top" -v head="## [$new] - $today" '!done && $0 == top { print head; done = 1; next } { print }' "$file" >"$file.new"
else
  new="$(next_free "${version%.*}.$(( ${version##*.} + 1 ))")"
  awk -v top="$top" -v head="## [$new] - $today" '!done && $0 == top { print head; print ""; print "- Maintenance release."; print ""; done = 1 } { print }' "$file" >"$file.new"
fi
mv "$file.new" "$file"
printf '%s\n' "$new"
