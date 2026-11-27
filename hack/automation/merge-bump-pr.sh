#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# merge-bump-pr.sh: merge stable core-bump proposals that change no proto.
#
# Usage: merge-bump-pr.sh [--dry-run] [--auto-merge on|off]
#
# A stable core release whose bindings are unchanged needs no SDK work, so
# its proposal can merge, and release-tag.yml tags the SDK release, without
# waiting for a review. A release that changes the protos waits for review:
# the SDK release may lag until the new features are wrapped and usable.
# On or off comes from --auto-merge or CORE_AUTO_MERGE (default off).
#
# Every condition is checked again on each run; labels and the PR body are
# not trusted. A proposal is merged only when all of these hold:
#   - open, not a draft, from branch core/vX.Y.Z of this repository into main
#   - no "hold" label (a maintainer's way to keep it for review)
#   - main still pins the tag the proposal was cut from, and the branch's tag
#     is newer: after one proposal merges, an older one sharing its base
#     would only conflict, and a downgrade always waits for review
#   - exactly one commit since main, with a successful provenance check from
#     the bump App on that exact SHA
#   - the commit's tree is exactly the tree pin_bump_tree (pin-bump.sh)
#     rebuilds from the merge base: the pin in buf.gen.yaml set to the
#     branch's tag and the README line updated, nothing else. No file under
#     proto/ changed (the generated bindings are identical), no conformance
#     fixture changed, and no other edit to buf.gen.yaml, such as a plugin.
#   - the checks in required_checks passed and none failed or is pending
# A refused merge leaves that proposal for review and the run goes on with
# the others; the script exits non-zero at the end so the failure is reported.
# Repository rules limit writes to core/*, enforce required checks without
# bypass, and let the merge App bypass code owner review on pull requests.
# Run from the root of an sdk-go clone.
set -euo pipefail

sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
bump_app_id=5613773 # ai-openshell-sdk-go GitHub App
provenance_check=core-bump/provenance
required_checks=("ci" "e2e / e2e" "DCO")
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/automation/pin-bump.sh
. "$here/pin-bump.sh"

dry=0
mode=${CORE_AUTO_MERGE:-off}
while [ $# -gt 0 ]; do
  case $1 in
  --dry-run) dry=1; shift ;;
  --auto-merge) mode=${2:?--auto-merge needs on or off}; shift 2 ;;
  *) echo "usage: $0 [--dry-run] [--auto-merge on|off]" >&2; exit 2 ;;
  esac
done
case $mode in
off | "") echo "Auto-merge is off (CORE_AUTO_MERGE=${mode:-unset})."; exit 0 ;;
on) ;;
*) echo "ERROR: CORE_AUTO_MERGE must be on or off, not '$mode'" >&2; exit 2 ;;
esac

die() { echo "ERROR: $*" >&2; exit 1; }
[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || die "run this from the root of the sdk-go clone"

prs=$(gh pr list -R "$sdk_repo" --label core-bump --label stable --state open \
  --json number,headRefName,headRefOid,baseRefName,isDraft,isCrossRepository,labels \
  --jq '.[] | [.number, .headRefName, .headRefOid, .baseRefName, .isDraft, .isCrossRepository,
                ([.labels[].name] | index("hold") != null)] | @tsv')
[ -n "$prs" ] || { echo "No open stable core-bump proposals."; exit 0; }

failed=0
while IFS=$'\t' read -r n head sha base draft cross held; do
  skip() { echo "#$n: $*; leaving it for review."; }
  tag=${head#core/}

  [ "$base" = main ] || { skip "base is $base, not main"; continue; }
  [ "$draft" = false ] || { skip "draft"; continue; }
  [ "$cross" = false ] || { skip "head is in a fork"; continue; }
  [ "$held" = false ] || { skip "labeled hold"; continue; }
  [[ "$head" =~ ^core/v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { skip "branch $head is not core/vX.Y.Z"; continue; }

  # Fetch main for each proposal: an earlier one in this run may have moved
  # the pin.
  git fetch --quiet origin "+refs/heads/main:refs/remotes/origin/main" \
    "+refs/heads/$head:refs/remotes/origin/$head"
  [ "$(git rev-parse "origin/$head")" = "$sha" ] || { skip "head moved during the run"; continue; }
  mb=$(git merge-base origin/main "$sha")

  main_pin=$(git show origin/main:buf.gen.yaml | yq -r '.inputs[0].tag')
  mb_pin=$(git show "$mb:buf.gen.yaml" | yq -r '.inputs[0].tag')
  [ "$main_pin" = "$mb_pin" ] || { skip "main moved the pin to $main_pin since this proposal was cut"; continue; }
  older "$main_pin" "$tag" || { skip "$tag is not newer than the pin $main_pin on main"; continue; }

  count=$(git rev-list --count "$mb..$sha")
  [ "$count" = 1 ] || { skip "$count commits, expected 1"; continue; }

  if ! gh api --method GET "repos/$sdk_repo/commits/$sha/check-runs" \
    -f check_name="$provenance_check" -F app_id="$bump_app_id" |
    jq -e --arg name "$provenance_check" --arg sha "$sha" --argjson app "$bump_app_id" \
      'any(.check_runs[]; .name == $name and .app.id == $app and .head_sha == $sha and .conclusion == "success")' >/dev/null; then
    skip "no successful bump App provenance for $sha"
    continue
  fi

  # Compare whole trees rather than file names: an edit inside an allowed
  # file (a Buf plugin entry in buf.gen.yaml, say) must not pass.
  expected=$(pin_bump_tree "$mb" "$tag" stable)
  if [ "$expected" != "$(git rev-parse "$sha^{tree}")" ]; then
    diff=$(git diff --name-only "$expected" "$sha")
    if grep -q '^proto/' <<<"$diff"; then
      skip "the bindings change ($(grep -c '^proto/' <<<"$diff") files under proto/), so the new core API needs review"
    else
      skip "differs from the pin bump to $tag in: $(tr '\n' ' ' <<<"$diff")"
    fi
    continue
  fi

  # gh pr checks exits non-zero while checks fail or are pending; read the
  # buckets instead. Each required check must be present and pass, and no
  # other check may fail or be pending.
  checks=$(gh pr checks "$n" -R "$sdk_repo" --json name,bucket 2>/dev/null || true)
  [ -n "$checks" ] || { skip "no checks reported yet"; continue; }
  missing=
  for c in "${required_checks[@]}"; do
    jq -e --arg c "$c" 'any(.[]; .name == $c and .bucket == "pass")' <<<"$checks" >/dev/null || missing+="'$c' "
  done
  [ -z "$missing" ] || { skip "required checks not passed: $missing"; continue; }
  bad=$(jq -r '[.[] | select(.bucket != "pass" and .bucket != "skipping") | "\(.name) (\(.bucket))"] | join(", ")' <<<"$checks")
  [ -z "$bad" ] || { skip "checks not green: $bad"; continue; }

  comment=$(printf 'Merged without review by core-watch: core `%s` changes no proto, the bump App vouched for the head SHA, and required checks passed.\n\nMerging tags the SDK release `%s`.' \
    "$tag" "$tag")

  if [ "$dry" -eq 1 ]; then
    echo "dry-run: would merge #$n ($tag) at $sha with comment:"
    sed 's/^/  | /' <<<"$comment"
    continue
  fi
  # --match-head-commit refuses the merge if someone pushed since the checks.
  # --admin uses the merge App's ruleset bypass for the code owner review.
  # Comment only after the merge, so a refused merge leaves no false claim.
  if ! gh pr merge "$n" -R "$sdk_repo" --squash --admin --match-head-commit "$sha"; then
    echo "ERROR: #$n: merging $tag failed; leaving it for review." >&2
    failed=1
    continue
  fi
  echo "#$n: merged ($tag)."
  gh pr comment "$n" -R "$sdk_repo" --body "$comment" ||
    echo "WARNING: #$n merged, but the comment explaining why failed." >&2
done <<<"$prs"

exit "$failed"
