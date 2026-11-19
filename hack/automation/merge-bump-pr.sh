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
#   - exactly one commit since main, authored and committed by the bot
#     identity in git config (the one core-watch sets), so any human push
#     sends it back to review
#   - the pin in buf.gen.yaml is the branch's tag, and the commit touches
#     only buf.gen.yaml and README.md: no file under proto/ changed (the
#     generated bindings are identical), and no conformance fixture changed
#   - the checks in required_checks passed and none failed or is pending
# The bot must be a bypass actor (pull requests only) on the main-integrity
# ruleset, since the merge skips the code owner review.
# Run from the root of an sdk-go clone. GH_TOKEN comes from the environment.
set -euo pipefail

sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
required_checks=("ci" "e2e / e2e" "DCO")
allowed_paths='^(buf\.gen\.yaml|README\.md)$'

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
bot_email=$(git config user.email) || die "git user.email is not set; it names the bot identity"

git fetch --quiet origin main

prs=$(gh pr list -R "$sdk_repo" --label core-bump --label stable --state open \
  --json number,headRefName,headRefOid,baseRefName,isDraft,isCrossRepository,labels \
  --jq '.[] | [.number, .headRefName, .headRefOid, .baseRefName, .isDraft, .isCrossRepository,
                ([.labels[].name] | index("hold") != null)] | @tsv')
[ -n "$prs" ] || { echo "No open stable core-bump proposals."; exit 0; }

while IFS=$'\t' read -r n head sha base draft cross held; do
  skip() { echo "#$n: $*; leaving it for review."; }
  tag=${head#core/}

  [ "$base" = main ] || { skip "base is $base, not main"; continue; }
  [ "$draft" = false ] || { skip "draft"; continue; }
  [ "$cross" = false ] || { skip "head is in a fork"; continue; }
  [ "$held" = false ] || { skip "labeled hold"; continue; }
  [[ "$head" =~ ^core/v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { skip "branch $head is not core/vX.Y.Z"; continue; }

  git fetch --quiet origin "+refs/heads/$head:refs/remotes/origin/$head"
  [ "$(git rev-parse "origin/$head")" = "$sha" ] || { skip "head moved during the run"; continue; }
  mb=$(git merge-base origin/main "$sha")

  count=$(git rev-list --count "$mb..$sha")
  [ "$count" = 1 ] || { skip "$count commits, expected 1"; continue; }
  idents=$(git log -1 --format='%ae %ce' "$sha")
  [ "$idents" = "$bot_email $bot_email" ] || { skip "commit is not the bot's ($idents)"; continue; }

  pin=$(git show "$sha:buf.gen.yaml" | yq -r '.inputs[0].tag')
  [ "$pin" = "$tag" ] || { skip "pin is $pin, branch says $tag"; continue; }
  extra=$(git diff --name-only "$mb" "$sha" | grep -Ev "$allowed_paths" || true)
  if [ -n "$extra" ]; then
    if grep -q '^proto/' <<<"$extra"; then
      skip "the bindings change ($(grep -c '^proto/' <<<"$extra") files under proto/), so the new core API needs review"
    else
      skip "touches files outside the pin bump: $(tr '\n' ' ' <<<"$extra")"
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

  comment=$(printf 'Merged without review by core-watch: core `%s` changes no proto, so the generated bindings are identical; one bot commit, pin bump only, required checks green.\n\nMerging tags the SDK release `%s`.' \
    "$tag" "$tag")

  if [ "$dry" -eq 1 ]; then
    echo "dry-run: would merge #$n ($tag) at $sha with comment:"
    sed 's/^/  | /' <<<"$comment"
    continue
  fi
  gh pr comment "$n" -R "$sdk_repo" --body "$comment"
  # --match-head-commit refuses the merge if someone pushed since the checks.
  # --admin uses the bot's ruleset bypass for the code owner review.
  gh pr merge "$n" -R "$sdk_repo" --squash --admin --match-head-commit "$sha"
  echo "#$n: merged ($tag)."
done <<<"$prs"
