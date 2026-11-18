#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# rpc-issues-test.sh: offline tests for the RPC coverage issues.
#
# Usage: rpc-issues-test.sh [--update]
#
# Run from the root of an sdk-go clone. Runs the planner (rpc-issues.jq) on
# every hack/automation/testdata/rpc-issues/planner/<case>/input.json and
# compares the result with expected.json, checks the coverage helper on the
# miniature SDK tree in coverage/, and the exceptions parser on exceptions/.
# --update rewrites the planner expected.json files instead of comparing.
# Exits 1 on any mismatch.
set -euo pipefail

here=hack/automation
data=$here/testdata/rpc-issues
# shellcheck source=hack/automation/rpc-issues-lib.sh
. "$here/rpc-issues-lib.sh"
# shellcheck source=hack/automation/rpc-coverage.sh
. "$here/rpc-coverage.sh"

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
  if ! jq -S -f "$here/rpc-issues.jq" --rawfile template "$here/rpc-issue-body.md" \
    "$dir/input.json" >"$work/actual.json" 2>"$work/err"; then
    fail "$name: planner failed: $(cat "$work/err")"
    continue
  fi
  if [ "$update" -eq 1 ]; then
    cp "$work/actual.json" "$dir/expected.json"
    [ -f "$dir/expected.json.license" ] || cp "$dir/input.json.license" "$dir/expected.json.license" 2>/dev/null || true
    echo "updated $name"
    continue
  fi
  [ -f "$dir/expected.json" ] || { fail "$name: no expected.json (run with --update)"; continue; }
  jq -S . "$dir/expected.json" >"$work/expected.json"
  if diff -u "$work/expected.json" "$work/actual.json" >"$work/diff"; then
    echo "ok $name"
  else
    fail "$name"
    cat "$work/diff"
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
if rpc_parse_exceptions "$data/exceptions/missing-reason.txt" >/dev/null 2>"$work/err"; then
  fail "exceptions/missing-reason: parsed without error"
elif grep -q '^ERROR: .*missing-reason.txt:3: ' "$work/err"; then
  echo "ok exceptions/missing-reason"
else
  fail "exceptions/missing-reason: unexpected error: $(cat "$work/err")"
fi

[ "$failed" -eq 0 ] || { echo "rpc-issues tests failed" >&2; exit 1; }
echo "rpc-issues tests passed"
