#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 3 ]; then
  echo "usage: $0 <version> [date] [changelog-file]" >&2
  exit 1
fi

VERSION="${1#v}"
RELEASE_DATE="${2:-$(date +%Y-%m-%d)}"
CHANGELOG_FILE="${3:-CHANGELOG.md}"

if [ ! -f "$CHANGELOG_FILE" ]; then
  echo "changelog file not found: $CHANGELOG_FILE" >&2
  exit 1
fi

UNRELEASED_SECTION="$(
  awk '
    /^## \[Unreleased\]/ { in_section = 1; found = 1 }
    in_section && /^## \[/ && $0 != "## [Unreleased]" { exit }
    in_section { print }
    END {
      if (!found) {
        exit 2
      }
    }
  ' "$CHANGELOG_FILE"
)" || {
  echo "unreleased section not found in $CHANGELOG_FILE" >&2
  exit 1
}

SECTION_BODY="$(printf '%s\n' "$UNRELEASED_SECTION" | tail -n +2 | sed '/./,$!d')"

if ! printf '%s\n' "$SECTION_BODY" | grep -qE '^(### |- )'; then
  echo "unreleased section is empty" >&2
  exit 1
fi

VERSION_SECTION="$(mktemp)"
{
  echo "## [${VERSION}] - ${RELEASE_DATE}"
  echo ""
  printf '%s\n' "$SECTION_BODY" | awk 'NF { blank = 0; print; next } !blank { print; blank = 1 }'
  echo ""
} > "$VERSION_SECTION"

TEMP_FILE="$(mktemp)"
awk -v insert_file="$VERSION_SECTION" '
  BEGIN { inserted = 0; skip_unreleased = 0 }
  /^## \[Unreleased\]/ {
    print "## [Unreleased]"
    print ""
    inserted = 1
    while ((getline line < insert_file) > 0) {
      print line
    }
    close(insert_file)
    skip_unreleased = 1
    next
  }
  skip_unreleased && /^## \[/ {
    skip_unreleased = 0
  }
  {
    if (skip_unreleased) {
      next
    }
    print
  }
  END {
    if (!inserted) {
      exit 3
    }
  }
' "$CHANGELOG_FILE" > "$TEMP_FILE"

mv "$TEMP_FILE" "$CHANGELOG_FILE"
