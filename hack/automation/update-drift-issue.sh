#!/usr/bin/env bash
# update-drift-issue.sh: keep the single "Core main drift" issue current.
#
# Usage: update-drift-issue.sh <report.md> [--dry-run]
#
# The report comes from drift-check.sh. The issue body is the report plus the
# "## Automation failures" section, which is carried over unchanged. With
# "Result: fail" or pending failures, the issue is created or its body
# replaced. With "Result: pass" and no pending failures, an open issue is
# closed with a comment, and nothing is created.
set -euo pipefail
# shellcheck source=hack/automation/drift-issue.sh
. "$(dirname "${BASH_SOURCE[0]}")/drift-issue.sh"

report=
dry=0
for a in "$@"; do
  case $a in
  --dry-run) dry=1 ;;
  -*) echo "usage: $0 <report.md> [--dry-run]" >&2; exit 2 ;;
  *) report=$a ;;
  esac
done
[ -f "$report" ] || { echo "usage: $0 <report.md> [--dry-run]" >&2; exit 2; }

text=$(cat "$report")
header=$(head -1 <<<"$text")
case $header in
*"Result: pass"*) result=pass ;;
*"Result: fail"*) result=fail ;;
*) echo "ERROR: $report has no 'Result: pass|fail' header" >&2; exit 1 ;;
esac

n=$(drift_issue_number)
failures=
[ -z "$n" ] || failures=$(failure_lines "$(gh issue view "$n" -R "$sdk_repo" --json body --jq .body)")
body=$(compose_body "$text" "$failures")

if [ "$result" = pass ] && [ -z "$failures" ]; then
  if [ -z "$n" ]; then
    echo "Drift check passes and no failures are pending; no issue needed."
    exit 0
  fi
  echo "Closing #$n: the drift check passes and no failures are pending."
  [ "$dry" -eq 1 ] && exit 0
  gh issue edit "$n" -R "$sdk_repo" --body "$body" >/dev/null
  gh issue close "$n" -R "$sdk_repo" --comment "The drift check passes against core main ($(sed 's/.*Core main: \([0-9a-f]*\).*/\1/' <<<"$header")) and no automation failures are pending."
  exit 0
fi

if [ "$dry" -eq 1 ]; then
  if [ -n "$n" ]; then target="update #$n"; else target="create the issue"; fi
  echo "dry-run: would $target with:"
  sed 's/^/  | /' <<<"$body"
  exit 0
fi
if [ -n "$n" ]; then
  gh issue edit "$n" -R "$sdk_repo" --body "$body" >/dev/null
  echo "Updated #$n."
else
  gh issue create -R "$sdk_repo" --title "$drift_title" --label "$drift_label" --body "$body"
fi
