#!/usr/bin/env bash
# open-bump-pr.sh: open the update proposal that moves the core pin to <tag>.
#
# Usage: open-bump-pr.sh <tag> [--dry-run] [--base <ref>]
#
# Run from the root of an sdk-go clone. Builds branch core/<tag> from
# origin/main (or --base) in a temporary worktree: pin change, regenerated
# bindings, refreshed conformance fixtures, one commit
# "chore(core): track <tag>". Then pushes it and opens a PR labeled
# core-bump plus stable or prerelease, and closes open pre-release proposals
# for older tags. --dry-run does the local part, prints the PR body, and
# changes nothing on GitHub.
#
# Skips the tag when branch core/<tag> exists on origin or any PR from it
# exists (merged or closed), so a closed proposal is never reopened. When the
# core tag no longer exists, closes an open proposal for it.
# Git identity and GH_TOKEN come from the environment (the App in CI).
set -euo pipefail

core_repo=${CORE_REPO:-NVIDIA/OpenShell}
sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
image=ghcr.io/nvidia/openshell/gateway
diff_cap=400

tag=
dry=0
base=origin/main
while [ $# -gt 0 ]; do
  case $1 in
  --dry-run) dry=1; shift ;;
  --base) base=${2:?--base needs a ref}; shift 2 ;;
  -*) echo "usage: $0 <tag> [--dry-run] [--base <ref>]" >&2; exit 2 ;;
  *) [ -z "$tag" ] || { echo "usage: $0 <tag> [--dry-run]" >&2; exit 2; }; tag=$1; shift ;;
  esac
done
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-pre\.[0-9]+)?$ ]] || {
  echo "ERROR: '$tag' is not a core release tag (vX.Y.Z or vX.Y.Z-pre.N)" >&2
  exit 2
}

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
body_tmpl="$here/bump-pr-body.md"
branch="core/$tag"
case $tag in *-pre.*) kind=prerelease ;; *) kind=stable ;; esac

die() { echo "ERROR: $*" >&2; exit 1; }

# older <a> <b>: true when release tag a sorts below b (vX.Y.Z-pre.N < vX.Y.Z).
older() {
  awk -v a="$1" -v b="$2" '
    function key(t,   s, pre, rank, v) {
      s = t; sub(/^v/, "", s); pre = 0; rank = 1
      if (index(s, "-pre.")) { pre = substr(s, index(s, "-pre.") + 5) + 0; s = substr(s, 1, index(s, "-pre.") - 1); rank = 0 }
      split(s, v, ".")
      return sprintf("%06d%06d%06d%d%06d", v[1], v[2], v[3], rank, pre)
    }
    BEGIN { exit !(key(a) < key(b)) }'
}
run() { if [ "$dry" -eq 1 ]; then echo "dry-run: $*"; else "$@"; fi; }

[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || die "run this from the root of the sdk-go clone"
[ -f "$body_tmpl" ] || die "$body_tmpl is missing"

open_pr_for_branch() {
  gh pr list -R "$sdk_repo" --head "$branch" --state open --json number --jq '.[0].number // empty'
}

# The core tag may have been deleted (a withdrawn pre-release). Only a 404
# means that; any other failure (rate limit, 5xx) stops the run.
if ! err=$(gh api "repos/$core_repo/git/ref/tags/$tag" --silent 2>&1); then
  grep -q 'HTTP 404' <<<"$err" || die "looking up core tag $tag failed: $err"
  n=$(open_pr_for_branch)
  if [ -n "$n" ]; then
    echo "Core tag $tag is gone; closing #$n."
    run gh pr close "$n" -R "$sdk_repo" --delete-branch \
      --comment "Core tag \`$tag\` no longer exists in $core_repo, so this proposal is closed."
  else
    echo "Core tag $tag does not exist; nothing to do."
  fi
  exit 0
fi

if [ -n "$(git ls-remote --heads origin "$branch")" ]; then
  echo "Branch $branch exists on origin; skipping $tag."
  exit 0
fi
# Any earlier proposal is final: merged, closed by a maintainer, or closed as
# superseded by a newer pre-release. Its branch may be gone, so ask for PRs.
prev=$(gh pr list -R "$sdk_repo" --head "$branch" --state all --json number,state \
  --jq '.[0] | select(.) | "#\(.number) (\(.state | ascii_downcase))"')
if [ -n "$prev" ]; then
  echo "Proposal $prev exists for $tag already; skipping."
  exit 0
fi

git fetch --quiet origin
git rev-parse --verify --quiet "$base^{commit}" >/dev/null || die "base $base is not a commit"
old=$(git show "$base:buf.gen.yaml" | yq -r '.inputs[0].tag')
[ "$old" != "$tag" ] || { echo "The pin on $base is $tag already; skipping."; exit 0; }

work=$(mktemp -d)
cleanup() { git worktree remove --force "$work/wt" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
git worktree add --quiet -b "$branch" "$work/wt" "$base"

(
  cd "$work/wt"
  yq -i ".inputs[0].tag = \"$tag\"" buf.gen.yaml
  mise run --quiet proto:gen >/dev/null

  # Refresh vendored fixtures; one not in the tag yet is left as is.
  : >"$work/fixtures.txt"
  for local in $(grep -v '^#' hack/conformance-fixtures.txt); do
    name=$(basename "$local")
    status=$(curl -sS -o "$work/fixture" -w '%{http_code}' \
      "https://raw.githubusercontent.com/$core_repo/$tag/sdk/conformance/$name")
    case $status in
    200)
      if ! cmp -s "$work/fixture" "$local"; then
        cp "$work/fixture" "$local"
        echo "- \`$local\` updated from core \`$tag\`" >>"$work/fixtures.txt"
      fi
      ;;
    404) echo "- \`$local\` is not in core \`$tag\` yet, left unchanged" >>"$work/fixtures.txt" ;;
    *) echo "ERROR: fetching $name at $tag returned HTTP $status" >&2; exit 1 ;;
    esac
  done

  # Keep the README's "generated from OpenShell core vX.Y.Z" line in step
  # with stable pins.
  if [ "$kind" = stable ]; then
    sed -i.bak "s/generated from OpenShell core ${old}/generated from OpenShell core ${tag}/" README.md && rm -f README.md.bak
  fi

  git add -A
  git commit --quiet --signoff -m "chore(core): track $tag" -m "Move the core pin from $old to $tag and regenerate the bindings."
)

# Proto diff between the two core tags. The compare API caps its file list at
# 300 entries, so fetch both tags shallow and blobless and diff locally; only
# the proto/ blobs are downloaded.
compare_url="https://github.com/$core_repo/compare/$old...$tag"
git init --quiet --bare "$work/core.git"
git -C "$work/core.git" fetch --quiet --depth=1 --filter=blob:none --no-tags \
  "https://github.com/$core_repo.git" "refs/tags/$old:refs/tags/$old" "refs/tags/$tag:refs/tags/$tag"
proto_diff=$(git -C "$work/core.git" diff "$old" "$tag" -- proto/)
if [ -z "$proto_diff" ]; then
  proto_section="No changes under \`proto/\` between \`$old\` and \`$tag\`."
else
  lines=$(wc -l <<<"$proto_diff" | tr -d ' ')
  shown=$(head -n "$diff_cap" <<<"$proto_diff")
  proto_section=$(printf '```diff\n%s\n```' "$shown")
  if [ "$lines" -gt "$diff_cap" ]; then
    proto_section=$(printf '%s\n\nShowing %s of %s lines; see the [full compare](%s).' "$proto_section" "$diff_cap" "$lines" "$compare_url")
  fi
fi
generated=$(git diff --stat "$base" "$branch" -- proto/ | tail -1 | sed "s/^ *//")
fixtures=$(cat "$work/fixtures.txt")

body="$work/body.md"
OLD="$old" NEW="$tag" KIND="$kind" COMPARE_URL="$compare_url" IMAGE="$image:${tag#v}" \
GENERATED="${generated:-no generated files changed}" FIXTURES="${fixtures:-- no fixture changes}" \
PROTO_DIFF="$proto_section" \
  awk '{
    while (match($0, /\{\{[A-Z_]+\}\}/)) {
      key = substr($0, RSTART + 2, RLENGTH - 4)
      $0 = substr($0, 1, RSTART - 1) ENVIRON[key] substr($0, RSTART + RLENGTH)
    }
    print
  }' "$body_tmpl" >"$body"

echo "Proposal for $tag ($kind), pin $old -> $tag:"
git log --format='  %h %an <%ae>  %s' "$base..$branch"
git diff --stat "$base" "$branch" | tail -1 | sed 's/^/  /'

if [ "$dry" -eq 1 ]; then
  echo
  echo "PR title: chore(core): track $tag"
  echo "PR body:"
  sed 's/^/  | /' "$body"
  cleanup
  git branch -D "$branch" >/dev/null
  echo
  echo "dry-run: nothing pushed; local branch $branch removed."
  exit 0
fi

git push --quiet origin "$branch"
url=$(gh pr create -R "$sdk_repo" --base main --head "$branch" \
  --title "chore(core): track $tag" --body-file "$body" \
  --label core-bump --label "$kind")
echo "Opened $url"
new=${url##*/}

# Close open pre-release proposals for older tags; this one supersedes them.
gh pr list -R "$sdk_repo" --label core-bump --label prerelease --state open \
  --json number,headRefName --jq '.[] | "\(.number) \(.headRefName)"' |
  while read -r n head; do
    other=${head#core/}
    [ "$n" != "$new" ] && [ "$other" != "$tag" ] || continue
    if older "$other" "$tag"; then
      echo "Closing #$n ($other), superseded by #$new."
      gh pr close "$n" -R "$sdk_repo" --delete-branch \
        --comment "Superseded by #$new, which tracks core \`$tag\`."
    fi
  done
