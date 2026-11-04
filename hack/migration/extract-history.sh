#!/usr/bin/env bash
# extract-history.sh: filter the sdk/go subtree history out of a
# NVIDIA/OpenShell clone at the freeze commit.
#
# Usage: extract-history.sh <core-clone> <freeze-sha> <out-dir>
#
# <out-dir> must not exist. Afterwards it is a standalone repository with one
# branch, main, whose tip tree is sdk/go at <freeze-sha>. Every commit that
# touched sdk/go keeps its author, date, and trailers. Bare #N references in
# commit messages become NVIDIA/OpenShell#N (see message-rules.txt).
# The last output line is the resulting HEAD.
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <core-clone> <freeze-sha> <out-dir>" >&2
  exit 2
fi

core=$1
freeze=$2
out=$3
rules="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/message-rules.txt"

command -v git-filter-repo >/dev/null 2>&1 || {
  echo "ERROR: git-filter-repo is not installed" >&2
  exit 1
}
[ -f "$rules" ] || {
  echo "ERROR: $rules is missing" >&2
  exit 1
}
[ ! -e "$out" ] || {
  echo "ERROR: $out exists; pass a path that does not exist yet" >&2
  exit 1
}
git -C "$core" cat-file -e "${freeze}^{commit}" 2>/dev/null || {
  echo "ERROR: $freeze is not a commit in $core" >&2
  exit 1
}
freeze=$(git -C "$core" rev-parse "${freeze}^{commit}")

# A real copy (no hardlinks, no tags, no checkout) that filter-repo may rewrite.
git clone --quiet --no-local --no-checkout --no-tags "$core" "$out"
# A freeze commit that only a remote-tracking ref reaches is not cloned.
git -C "$out" cat-file -e "${freeze}^{commit}" 2>/dev/null ||
  git -C "$out" fetch --quiet --no-tags "$core" "$freeze" || {
    echo "ERROR: $freeze is not reachable from a branch of $core" >&2
    exit 1
  }
git -C "$out" remote remove origin

# Keep exactly one branch, main, at the freeze commit.
git -C "$out" branch --force main "$freeze"
git -C "$out" symbolic-ref HEAD refs/heads/main
git -C "$out" for-each-ref --format='%(refname)' refs/heads |
  grep -v '^refs/heads/main$' |
  while read -r ref; do git -C "$out" update-ref -d "$ref"; done
git -C "$out" checkout --quiet main

# --force: <out-dir> is a throwaway copy without an origin, so filter-repo's
# fresh-clone check does not apply.
# The global and system config are hidden because filter-repo parses
# `git config --list` and chokes on keys without a value.
GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
  git -C "$out" filter-repo --force \
    --subdirectory-filter sdk/go \
    --replace-message "$rules"

echo "commits: $(git -C "$out" rev-list --count HEAD)"
echo "authors: $(git -C "$out" log --format='%an <%ae>' | sort -u | wc -l | tr -d ' ')"
echo "tree:    $(git -C "$out" rev-parse 'HEAD^{tree}')"
git -C "$out" rev-parse HEAD
