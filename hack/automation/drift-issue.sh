# SPDX-License-Identifier: Apache-2.0

# drift-issue.sh: shared helpers for the "Core main drift" issue. Sourced by
# update-drift-issue.sh, report-failure.sh, and rpc-issues-test.sh.
# shellcheck shell=bash

drift_automation_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

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

# add_failure_line <failure-lines> <date> <job> <run-url>: the failure lines
# with one more failed run recorded. A job that already has a line for the
# date keeps that one line, now naming the latest run and the number of
# failed runs ("- <date> <job> <url> (<n> runs)"), so an hourly job that keeps
# failing adds one line per day instead of one per run.
add_failure_line() {
  awk -v d="${2:?add_failure_line needs a date}" -v j="${3:?add_failure_line needs a job}" \
    -v u="${4:?add_failure_line needs a run URL}" '
    NF == 0 { next }
    !seen && $1 == "-" && $2 == d && $3 == j {
      seen = 1; n = 1
      if ($5 ~ /^\([0-9]+$/) n = substr($5, 2) + 0
      printf "- %s %s %s (%d runs)\n", d, j, u, n + 1
      next
    }
    { print }
    END { if (!seen) printf "- %s %s %s\n", d, j, u }' <<<"$1"
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

# link_uncovered_rpcs <issues.json> <report-text>: print the report with
# each "- Service.Method" line followed by a link to the RPC coverage issue
# whose marker (rpc-marker.jq) lists the method; the lowest number wins.
# issues.json is a JSON array of {number, body}.
link_uncovered_rpcs() {
  local links
  links=$(jq -L "$drift_automation_dir" -r 'include "rpc-marker";
    .[] | .number as $n | (.body | parse_marker) // empty | "\(.service).\(.methods[]) \($n)"' "$1" |
    sort -k2,2n) || return 1
  if [ -z "$links" ]; then
    printf '%s\n' "$2"
    return 0
  fi
  awk -v repo="$sdk_repo" '
    FILENAME == ARGV[1] { if (!($1 in issue)) issue[$1] = $2; next }
    /^- [A-Za-z0-9_]+\.[A-Za-z0-9_]+$/ && (substr($0, 3) in issue) {
      n = issue[substr($0, 3)]
      print $0 " ([#" n "](https://github.com/" repo "/issues/" n "))"
      next
    }
    { print }' <(printf '%s\n' "$links") <(printf '%s\n' "$2")
}
