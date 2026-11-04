#!/usr/bin/env bash
# report-failure.sh: record a failed automation run.
#
# Usage: report-failure.sh <job> <run-url>
#
# Until the drift issue exists (core-drift.yml), the failure goes to the job
# summary only; update-drift-issue.sh replaces this with a dated line in the
# "Core main drift" issue.
set -euo pipefail
[ $# -eq 2 ] || { echo "usage: $0 <job> <run-url>" >&2; exit 2; }
line="- $(date -u +%Y-%m-%d) $1 $2"
echo "Automation failure: $line"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "$line" >>"$GITHUB_STEP_SUMMARY"
fi
