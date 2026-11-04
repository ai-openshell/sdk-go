#!/usr/bin/env bash
# verify-tree.sh: check an imported history against the core subtree.
#
# Usage: verify-tree.sh <core-clone> <freeze-sha> <sdk-go-clone> <commit>
#
# Checks:
#   1. tree identity: <commit>^{tree} in sdk-go equals <freeze-sha>:sdk/go in core
#   2. completeness: the import has at least as many non-merge commits, distinct
#      authors, and Signed-off-by trailers as the sdk/go history in core
#      (equal for the filtered history; the import merge adds the bot's initial
#      commit and the merge commit on top)
#   3. references: no bare #N reference remains in any commit message
# Exits non-zero on any mismatch.
set -euo pipefail

if [ $# -ne 4 ]; then
  echo "usage: $0 <core-clone> <freeze-sha> <sdk-go-clone> <commit>" >&2
  exit 2
fi

core=$1
freeze=$2
sdk=$3
commit=$4
fail=0

want=$(git -C "$core" rev-parse "${freeze}:sdk/go")
got=$(git -C "$sdk" rev-parse "${commit}^{tree}")
echo "core sdk/go tree at ${freeze}: ${want}"
echo "import tree at ${commit}:      ${got}"
if [ "$want" = "$got" ]; then
  echo "tree: match"
else
  echo "tree: MISMATCH"
  fail=1
fi

core_commits=$(git -C "$core" rev-list --count --no-merges "$freeze" -- sdk/go)
import_commits=$(git -C "$sdk" rev-list --count --no-merges "$commit")
core_authors=$(git -C "$core" log --format='%an <%ae>' "$freeze" -- sdk/go | sort -u | wc -l | tr -d ' ')
import_authors=$(git -C "$sdk" log --format='%an <%ae>' "$commit" | sort -u | wc -l | tr -d ' ')
core_signoffs=$(git -C "$core" log --format=%B "$freeze" -- sdk/go | grep -c '^Signed-off-by:' || true)
import_signoffs=$(git -C "$sdk" log --format=%B "$commit" | grep -c '^Signed-off-by:' || true)

echo "commits:   core ${core_commits}, import ${import_commits}"
echo "authors:   core ${core_authors}, import ${import_authors}"
echo "sign-offs: core ${core_signoffs}, import ${import_signoffs}"
[ "$import_commits" -ge "$core_commits" ] || { echo "commits: MISSING"; fail=1; }
[ "$import_authors" -ge "$core_authors" ] || { echo "authors: MISSING"; fail=1; }
[ "$import_signoffs" -ge "$core_signoffs" ] || { echo "sign-offs: MISSING"; fail=1; }

bare=$(git -C "$sdk" log --format='--- %h%n%B' "$commit" | grep -nE '(^|[^A-Za-z0-9/#])#[0-9]+' || true)
if [ -n "$bare" ]; then
  echo "bare references: FOUND"
  echo "$bare"
  fail=1
else
  echo "bare references: none"
fi

rewritten=$(git -C "$sdk" log --format=%B "$commit" | grep -c 'NVIDIA/OpenShell#' || true)
echo "rewritten references: ${rewritten}"

if [ "$fail" -eq 0 ]; then
  echo "RESULT: pass"
else
  echo "RESULT: fail"
fi
exit "$fail"
