# drift-issue.sh: shared helpers for the "Core main drift" issue. Sourced by
# update-drift-issue.sh and report-failure.sh.
# shellcheck shell=bash

sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
drift_title="Core main drift"
drift_label=core-drift
failures_heading="## Automation failures"

# Prints the number of the open drift issue, or nothing.
drift_issue_number() {
  gh issue list -R "$sdk_repo" --label "$drift_label" --state open \
    --json number,title --jq ".[] | select(.title == \"$drift_title\") | .number" | head -1
}

# Prints the "- <date> <job> <run-url>" lines of a body's failures section.
failure_lines() {
  awk -v h="$failures_heading" '
    $0 == h { f = 1; next }
    f && /^## / { f = 0 }
    f && /^- / { print }' <<<"$1"
}

# compose_body <report-text> <failure-lines>
compose_body() {
  printf '%s\n\n%s\n\n' "$1" "$failures_heading"
  if [ -n "$2" ]; then
    printf '%s\n\n' "$2"
  else
    printf 'None.\n\n'
  fi
  printf '%s\n' "Automation appends a dated line here when core-watch, release-tag, or core-drift fails. Delete a line once it is handled; the issue closes when the drift check passes and no lines are left."
}
