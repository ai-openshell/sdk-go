#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# rpc-issues-test.sh: offline tests for the RPC coverage issues.
#
# Usage: rpc-issues-test.sh [--update]
#
# Run from the root of an sdk-go clone. Runs the planner (rpc-issues.jq) on
# every hack/automation/testdata/rpc-issues/planner/<case>/input.json and
# compares the result with expected.json (and, where present, its
# needs-actors mode with expected-actors.json), runs rpc-issues.sh offline
# with --plan-input, checks the coverage helper on the miniature SDK tree in
# coverage/, the exceptions parser on exceptions/, and the drift issue links
# on drift-links/. --update rewrites the planner expected files instead of
# comparing.
# Exits 1 on any mismatch.
set -euo pipefail

here=hack/automation
data=$here/testdata/rpc-issues
# shellcheck source=hack/automation/rpc-issues-lib.sh
. "$here/rpc-issues-lib.sh"
# shellcheck source=hack/automation/rpc-coverage.sh
. "$here/rpc-coverage.sh"
# shellcheck source=hack/automation/drift-issue.sh
. "$here/drift-issue.sh"

die() { echo "ERROR: $*" >&2; exit 1; }

update=0
while [ $# -gt 0 ]; do
  case $1 in
  --update) update=1; shift ;;
  *) echo "usage: $0 [--update]" >&2; exit 2 ;;
  esac
done
[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || die "run this from the root of the sdk-go clone"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() { echo "FAIL $1"; failed=1; }

# Planner cases.
for dir in "$data"/planner/*/; do
  [ -d "$dir" ] || continue
  name=$(basename "$dir")
  [ -f "$dir/input.json" ] || { fail "$name: no input.json"; continue; }
  for mode in plan needs-actors; do
    exp=expected.json label=$name
    if [ "$mode" = needs-actors ]; then
      exp=expected-actors.json label="$name (needs-actors)"
      [ -f "$dir/$exp" ] || continue
    fi
    if ! jq -S -L "$here" -f "$here/rpc-issues.jq" --rawfile template "$here/rpc-issue-body.md" \
      --arg mode "$mode" "$dir/input.json" >"$work/actual.json" 2>"$work/err"; then
      fail "$label: planner failed: $(cat "$work/err")"
      continue
    fi
    if [ "$update" -eq 1 ]; then
      cp "$work/actual.json" "$dir/$exp"
      [ -f "$dir/$exp.license" ] || cp "$dir/input.json.license" "$dir/$exp.license" 2>/dev/null || true
      echo "updated $label"
      continue
    fi
    [ -f "$dir/$exp" ] || { fail "$label: no $exp (run with --update)"; continue; }
    jq -S . "$dir/$exp" >"$work/expected.json"
    if diff -u "$work/expected.json" "$work/actual.json" >"$work/diff"; then
      echo "ok $label"
    else
      fail "$label"
      cat "$work/diff"
    fi
  done
done

# rpc-issues.sh offline: the issue limit, its override, and the dry-run output.
rpc_issues() { "$here/rpc-issues.sh" --plan-input "$@" >"$work/out" 2>"$work/err"; }
if rpc_issues "$data/planner/too-many-new/input.json"; then
  fail "script too-many-new: exit 0"
elif [ "$(grep -c '^  - feat(sdk): wrap ' "$work/err")" = 11 ] && grep -q 'nothing was changed' "$work/err"; then
  echo "ok script too-many-new"
else
  fail "script too-many-new: $(cat "$work/err")"
fi
if rpc_issues "$data/planner/too-many-new/input.json" --max-new 11 &&
  grep -q '^dry-run: core v0.2.0: 11 to open, 0 to update, 0 to close, 0 to reopen$' "$work/out"; then
  echo "ok script max-new"
else
  fail "script max-new: $(cat "$work/out" "$work/err")"
fi
for c in "backfill-v0.1.9:^dry-run: open \"feat\\(sdk\\): wrap the ServiceDiscovery service \\(core v0\\.1\\.9\\)\" \\(labels help wanted, rpc-coverage, rpc:blocked\\)$" \
  "message-changed:^dry-run: update #20 method:OpenShell\\.ForwardTcp$" \
  "reopen-swaps-label:^dry-run: reopen #20 method:OpenShell\\.ForwardTcp \\(labels \\+rpc:ready -rpc:blocked\\) with comment:$" \
  "covered:^dry-run: close #[0-9]+ method:OpenShell\.[A-Za-z]+ with comment:$"; do
  if rpc_issues "$data/planner/${c%%:*}/input.json" && grep -qE "${c#*:}" "$work/out" && grep -q '^  | ' "$work/out"; then
    echo "ok script dry-run ${c%%:*}"
  else
    fail "script dry-run ${c%%:*}: $(cat "$work/out" "$work/err")"
  fi
done

# Coverage helper.
if rpc_covered_list "$data/coverage" "$data/coverage/keys.txt" >"$work/covered.txt" 2>"$work/err" &&
  diff -u "$data/coverage/expected.txt" "$work/covered.txt"; then
  echo "ok coverage"
else
  fail "coverage $(cat "$work/err")"
fi

# Exceptions parser.
if rpc_parse_exceptions "$data/exceptions/valid.txt" >"$work/exc.json" 2>"$work/err" &&
  jq -e 'type == "array" and length > 0 and all(.[]; (.key | test("^[A-Za-z0-9_]+\\.[A-Za-z0-9_]+$")) and (.reason | length > 0))' \
    "$work/exc.json" >/dev/null; then
  echo "ok exceptions/valid"
else
  fail "exceptions/valid $(cat "$work/err")"
fi
if jq -e '. == [{key: "OpenShell.ForwardTcp", reason: "The TCP client already forwards ports over SSH."},
               {key: "ServiceDiscovery.WatchServiceRoutes", reason: "Only ingress controllers consume routes."}]' \
  "$work/exc.json" >/dev/null; then
  echo "ok exceptions/valid entries"
else
  fail "exceptions/valid entries: $(cat "$work/exc.json")"
fi
for c in missing-reason:3:has.no.reason bad-key:3:is.not.Service.Method; do
  f=${c%%:*} rest=${c#*:}
  if rpc_parse_exceptions "$data/exceptions/$f.txt" >/dev/null 2>"$work/err"; then
    fail "exceptions/$f: parsed without error"
  elif grep -qE "^ERROR: .*$f.txt:${rest%%:*}: .*${rest#*:}" "$work/err"; then
    echo "ok exceptions/$f"
  else
    fail "exceptions/$f: unexpected error: $(cat "$work/err")"
  fi
done

# Only bot-opened issues are managed.
if rpc_bot_issues 'ai-openshell-sdk-go[bot]' "$data/bot-issues/issues.ndjson" >"$work/bot.json" 2>"$work/err" &&
  jq -e '[.[].number] == [1, 3] and all(.[]; has("author") | not)' "$work/bot.json" >/dev/null &&
  [ "$(rpc_bot_issues 'ai-openshell-sdk-go[bot]' </dev/null)" = "[]" ]; then
  echo "ok bot-issues"
else
  fail "bot-issues $(cat "$work/bot.json" "$work/err")"
fi

# Drift issue links to coverage issues.
if link_uncovered_rpcs "$data/drift-links/issues.json" "$(cat "$data/drift-links/report.md")" >"$work/linked.md" 2>"$work/err" &&
  diff -u "$data/drift-links/expected.md" "$work/linked.md"; then
  echo "ok drift-links"
else
  fail "drift-links $(cat "$work/err")"
fi

[ "$failed" -eq 0 ] || { echo "rpc-issues tests failed" >&2; exit 1; }
echo "rpc-issues tests passed"
