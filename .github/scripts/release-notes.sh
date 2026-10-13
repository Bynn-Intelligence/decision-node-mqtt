#!/usr/bin/env bash
# Prints the section of CHANGELOG.md for VERSION, without its heading
# and leading blank lines: the release notes of that version. Fails when the
# CHANGELOG has no such section or it is empty.
#
#   release-notes.sh VERSION
set -euo pipefail
version="${1:?usage: release-notes.sh VERSION}"
notes="$(awk -v head="## [$version]" '
  index($0, head) == 1 { on = 1; next }
  on && /^## \[/ { exit }
  on { print }
' CHANGELOG.md | sed -e '/./,$!d')"
if [ -z "$notes" ]; then
  echo "CHANGELOG.md has no notes for $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
