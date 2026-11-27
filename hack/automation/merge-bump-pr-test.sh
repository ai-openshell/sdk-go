#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# merge-bump-pr-test.sh: offline tests for the stable pin auto-merge.
#
# Usage: merge-bump-pr-test.sh
#
# Run from the root of an sdk-go clone. Checks pin_bump_tree and older
# (pin-bump.sh) in a scratch Git repository, then runs merge-bump-pr.sh
# against scratch repositories with a gh stub (testdata/merge-bump/bin/gh)
# that serves PR lists, check runs and checks from files and squash-merges
# into a local bare origin. Exits 1 on any failed case.
set -euo pipefail

here=$(pwd -P)/hack/automation
stub_bin=$here/testdata/merge-bump/bin
# shellcheck source=hack/automation/pin-bump.sh
. "$here/pin-bump.sh"

die() { echo "ERROR: $*" >&2; exit 1; }
[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || die "run this from the root of the sdk-go clone"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() { echo "FAIL $1"; failed=1; }
pass() { echo "ok   $1"; }

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# new_case <name>: a bare origin whose main pins core v0.1.10, and a clone
# of it in $repo. Sets $case_dir, $repo and $state (the gh stub's state).
new_case() {
  case_dir=$work/$1
  state=$case_dir/state
  repo=$case_dir/repo
  mkdir -p "$state" "$repo"
  git init --quiet --bare -b main "$state/origin.git"
  git -C "$repo" init --quiet -b main
  git -C "$repo" remote add origin "$state/origin.git"
  cat >"$repo/buf.gen.yaml" <<'EOF'
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: proto
inputs:
  - git_repo: https://github.com/ai-openshell/core
    tag: v0.1.10
EOF
  printf '# SDK\n\nThe bindings are generated from OpenShell core v0.1.10.\n' >"$repo/README.md"
  mkdir -p "$repo/proto"
  echo "package proto" >"$repo/proto/api.pb.go"
  git -C "$repo" add -A
  git -C "$repo" commit --quiet -m init
  git -C "$repo" push --quiet origin main
  echo '[]' >"$state/prs.json"
  echo '[{"name":"ci","bucket":"pass"},{"name":"e2e / e2e","bucket":"pass"},{"name":"DCO","bucket":"pass"}]' >"$state/checks.json"
  : >"$state/events"
}

# propose <n> <tag> [<edit command>...]: commit a pin bump to <tag> on
# core/<tag> from origin/main, run the optional extra edit in the clone
# before committing, push it, and list it as PR <n> (labels: $labels,
# draft: $draft, fork: $fork). Sets $sha.
propose() {
  local n=$1 tag=$2 kind=stable
  shift 2
  case $tag in *-pre.*) kind=prerelease ;; esac
  git -C "$repo" checkout --quiet -B "core/$tag" origin/main
  (cd "$repo" && pin_bump_edit "$tag" "$kind" && if [ $# -gt 0 ]; then "$@"; fi)
  git -C "$repo" commit --quiet -am "chore(core): track $tag"
  git -C "$repo" push --quiet origin "core/$tag"
  sha=$(git -C "$repo" rev-parse HEAD)
  git -C "$repo" checkout --quiet --detach origin/main
  jq --argjson n "$n" --arg head "core/$tag" --arg sha "$sha" \
    --argjson draft "${draft:-false}" --argjson fork "${fork:-false}" --arg labels "${labels:-core-bump,stable}" \
    '. + [{number: $n, headRefName: $head, headRefOid: $sha, baseRefName: "main",
           isDraft: $draft, isCrossRepository: $fork,
           labels: ($labels | split(",") | map({name: .}))}]' \
    "$state/prs.json" >"$state/prs.tmp"
  mv "$state/prs.tmp" "$state/prs.json"
}

# run_merge: run merge-bump-pr.sh in the clone; output in $out, status in $rc.
run_merge() {
  rc=0
  out=$(cd "$repo" && PATH="$stub_bin:$PATH" GH_STUB_STATE="$state" \
    "$here/merge-bump-pr.sh" --auto-merge on 2>&1) || rc=$?
}

main_pin() { git -C "$state/origin.git" show main:buf.gen.yaml | yq -r '.inputs[0].tag'; }
events() { paste -sd, "$state/events"; }

# expect <case> <condition description> <test command...>
expect() {
  local name=$1 what=$2
  shift 2
  if "$@"; then pass "$name: $what"; else
    fail "$name: $what"
    printf '  output:\n%s\n  events: %s\n' "$(sed 's/^/    /' <<<"$out")" "$(events)"
  fi
}
says() { grep -qF -- "$1" <<<"$out"; }

# --- older ---------------------------------------------------------------
for pair in "v0.1.9 v0.1.10" "v0.1.10-pre.2 v0.1.10" "v0.1.10-pre.2 v0.1.10-pre.10" "v0.9.0 v1.0.0"; do
  # shellcheck disable=SC2086 # two words on purpose
  if older $pair; then pass "older $pair"; else fail "older $pair"; fi
done
for pair in "v0.1.10 v0.1.10" "v0.1.10 v0.1.9" "v0.1.10 v0.1.10-pre.2"; do
  # shellcheck disable=SC2086
  if older $pair; then fail "not older $pair"; else pass "not older $pair"; fi
done

# --- pin_bump_tree -------------------------------------------------------
new_case tree
base=$(git -C "$repo" rev-parse HEAD)
propose 1 v0.1.11
expect tree "stable bump commit matches the rebuilt tree" \
  test "$(cd "$repo" && pin_bump_tree "$base" v0.1.11 stable)" = "$(git -C "$repo" rev-parse "$sha^{tree}")"
expect tree "the caller's index and worktree stay clean" \
  test -z "$(git -C "$repo" status --porcelain)"
propose 2 v0.1.12 yq -i '.plugins += [{"local": "protoc-gen-evil", "out": "proto"}]' buf.gen.yaml
expect tree "an extra Buf plugin does not match" \
  test "$(cd "$repo" && pin_bump_tree "$base" v0.1.12 stable)" != "$(git -C "$repo" rev-parse "$sha^{tree}")"
propose 3 v0.1.13 sh -c 'echo "// changed" >>proto/api.pb.go'
expect tree "a change under proto/ does not match" \
  test "$(cd "$repo" && pin_bump_tree "$base" v0.1.13 stable)" != "$(git -C "$repo" rev-parse "$sha^{tree}")"
pre_tree=$(cd "$repo" && pin_bump_tree "$base" v0.1.11-pre.1 prerelease)
expect tree "a pre-release bump leaves README.md unchanged" \
  test "$(git -C "$repo" rev-parse "$pre_tree:README.md")" = "$(git -C "$repo" rev-parse "$base:README.md")"
expect tree "a pre-release bump sets the pin" \
  test "$(git -C "$repo" show "$pre_tree:buf.gen.yaml" | yq -r '.inputs[0].tag')" = v0.1.11-pre.1

# --- merge-bump-pr.sh ----------------------------------------------------
# gh pr list returns the newest proposal first.
new_case two-proposals
propose 2 v0.1.12
propose 1 v0.1.11
run_merge
expect two-proposals "exits 0" test "$rc" = 0
expect two-proposals "merges the newest, then comments" test "$(events)" = "merge #2,comment #2"
expect two-proposals "main pins v0.1.12" test "$(main_pin)" = v0.1.12
expect two-proposals "leaves the older proposal for review" says "#1: main moved the pin to v0.1.12 since this proposal was cut"
run_merge
expect two-proposals "a second run leaves main alone" test "$(events)" = "merge #2,comment #2"

new_case refused
propose 2 v0.1.12
propose 1 v0.1.11
touch "$state/refuse-2"
run_merge
expect refused "exits 1" test "$rc" = 1
expect refused "posts no comment on the refused proposal" test "$(events)" = "merge #1,comment #1"
expect refused "reports the refused merge" says "ERROR: #2: merging v0.1.12 failed; leaving it for review."
expect refused "goes on and merges the next proposal" test "$(main_pin)" = v0.1.11

new_case downgrade
propose 1 v0.1.9
run_merge
expect downgrade "does not merge an older tag" test "$(events)" = ""
expect downgrade "says why" says "#1: v0.1.9 is not newer than the pin v0.1.10 on main"

new_case provenance
propose 1 v0.1.11
jq -n --arg sha "$sha" '{check_runs: [{name: "core-bump/provenance", app: {id: 42}, head_sha: $sha, conclusion: "success"}]}' >"$state/runs-$sha.json"
propose 2 v0.1.12
jq -n --arg sha "$sha" '{check_runs: [{name: "core-bump/provenance", app: {id: 5613773}, head_sha: $sha, conclusion: "failure"}]}' >"$state/runs-$sha.json"
propose 3 v0.1.13
jq -n '{check_runs: [{name: "core-bump/provenance", app: {id: 5613773}, head_sha: "0000000000000000000000000000000000000000", conclusion: "success"}]}' >"$state/runs-$sha.json"
propose 4 v0.1.14
echo '{"check_runs": []}' >"$state/runs-$sha.json"
run_merge
expect provenance "merges nothing" test "$(events)" = ""
for n in 1 2 3 4; do
  expect provenance "#$n skipped for provenance" says "#$n: no successful bump App provenance"
done

new_case tree-mismatch
propose 1 v0.1.11 yq -i '.plugins += [{"local": "protoc-gen-evil", "out": "proto"}]' buf.gen.yaml
propose 2 v0.1.12 sh -c 'echo "// changed" >>proto/api.pb.go'
run_merge
expect tree-mismatch "merges nothing" test "$(events)" = ""
expect tree-mismatch "a plugin edit goes to review" says "#1: differs from the pin bump to v0.1.11 in: buf.gen.yaml"
expect tree-mismatch "a proto change goes to review" says "#2: the bindings change (1 files under proto/)"

new_case checks
propose 1 v0.1.11
echo '[{"name":"ci","bucket":"pass"},{"name":"e2e / e2e","bucket":"pass"}]' >"$state/checks-1.json"
propose 2 v0.1.12
echo '[{"name":"ci","bucket":"pass"},{"name":"e2e / e2e","bucket":"skipping"},{"name":"DCO","bucket":"pass"}]' >"$state/checks-2.json"
propose 3 v0.1.13
echo '[{"name":"ci","bucket":"pass"},{"name":"e2e / e2e","bucket":"pass"},{"name":"DCO","bucket":"pass"},{"name":"lint","bucket":"pending"}]' >"$state/checks-3.json"
propose 4 v0.1.14
echo '[{"name":"ci","bucket":"pass"},{"name":"ci","bucket":"fail"},{"name":"e2e / e2e","bucket":"pass"},{"name":"DCO","bucket":"pass"}]' >"$state/checks-4.json"
run_merge
expect checks "merges nothing" test "$(events)" = ""
expect checks "a missing required check blocks" says "#1: required checks not passed: 'DCO'"
expect checks "a skipped required check blocks" says "#2: required checks not passed: 'e2e / e2e'"
expect checks "a pending check blocks" says "#3: checks not green: lint (pending)"
expect checks "a failed run of a required check blocks" says "#4: checks not green: ci (fail)"

new_case filters
labels=core-bump,stable,hold propose 1 v0.1.11
draft=true propose 2 v0.1.12
fork=true propose 3 v0.1.13
propose 4 v0.1.14-pre.1
run_merge
expect filters "merges nothing" test "$(events)" = ""
expect filters "hold" says "#1: labeled hold"
expect filters "draft" says "#2: draft"
expect filters "fork" says "#3: head is in a fork"
expect filters "pre-release branch" says "#4: branch core/v0.1.14-pre.1 is not core/vX.Y.Z"

[ "$failed" -eq 0 ] || { echo "merge-bump-pr-test: FAILED" >&2; exit 1; }
echo "merge-bump-pr-test: all cases passed."
