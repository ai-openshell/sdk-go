#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# report-failure.sh: record a failed automation run in the drift issue.
#
# Usage: report-failure.sh <job> <run-url> [--dry-run]
#
# Records "- <date> <job> <run-url>" in the "## Automation failures" section
# of the open "Core main drift" issue, creating the issue when none is open.
# Repeated failures of a job on the same date update its one line with the
# latest run and a count instead of adding a line per run.
# Also writes the line to the job summary.
set -euo pipefail
# shellcheck source=hack/automation/drift-issue.sh
. "$(dirname "${BASH_SOURCE[0]}")/drift-issue.sh"

dry=0
args=()
for a in "$@"; do
  case $a in --dry-run) dry=1 ;; *) args+=("$a") ;; esac
done
[ ${#args[@]} -eq 2 ] || { echo "usage: $0 <job> <run-url> [--dry-run]" >&2; exit 2; }
today=$(date -u +%Y-%m-%d)
line="- $today ${args[0]} ${args[1]}"
[ -z "${GITHUB_STEP_SUMMARY:-}" ] || echo "Automation failure: $line" >>"$GITHUB_STEP_SUMMARY"

n=$(drift_issue_number)
if [ -n "$n" ]; then
  current=$(gh issue view "$n" -R "$sdk_repo" --json body --jq .body)
  report=$(awk -v h="$failures_heading" '$0 == h { exit } { print }' <<<"$current" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')
  failures=$(failure_lines "$current")
else
  report="No drift report yet; the next core-drift run fills this in."
  failures=
fi
failures=$(add_failure_line "$failures" "$today" "${args[0]}" "${args[1]}")
body=$(compose_body "$report" "$failures")

if [ "$dry" -eq 1 ]; then
  if [ -n "$n" ]; then target="update #$n"; else target="create the issue"; fi
  echo "dry-run: would $target with:"
  sed 's/^/  | /' <<<"$body"
  exit 0
fi
if [ -n "$n" ]; then
  gh issue edit "$n" -R "$sdk_repo" --body "$body" >/dev/null
  echo "Recorded the failure in #$n."
else
  gh issue create -R "$sdk_repo" --title "$drift_title" --label "$drift_label" --body "$body"
fi
