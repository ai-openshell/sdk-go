#!/usr/bin/env bash
# port-pr.sh: port an open NVIDIA/OpenShell PR that touches sdk/go into this
# repository, keeping every commit with its original author, date, and
# Signed-off-by trailer.
#
# Usage: port-pr.sh [options] <core-pr-number>
#        port-pr.sh --fix-paths <core-pr-number>
#        port-pr.sh --continue [options] <core-pr-number>
#
# Options:
#   --base <ref>     branch the port starts from (default: origin/main)
#   --core <url>     core repository to fetch from
#                    (default: https://github.com/NVIDIA/OpenShell.git)
#   --push           push port/<n> and open the sdk-go PR (default: stop after
#                    the local branch and print the PR body for review)
#   --no-build       skip `go build ./...` and `go vet ./...` on the result
#   --fix-paths      during a stopped `git am`: rewrite the old module path on
#                    lines that differ from HEAD and stage those files
#   --continue       finish a port after resolving a failed `git am`
#
# Run it from the root of an sdk-go clone with a clean working tree.
#
# Steps:
#   1. Fetch the PR head and the core base branch into refs/port-src/<n>/.
#   2. format-patch the PR commits (merge-base..head) limited to sdk/go, with
#      the sdk/go/ prefix stripped.
#   3. Rewrite github.com/NVIDIA/OpenShell/sdk/go to github.com/ai-openshell/sdk-go
#      inside the patches, so diff context matches this tree.
#   4. git am --3way onto port/<n> from --base. On a conflict, resolve it,
#      run --fix-paths (a 3-way resolution comes from the core blob and still
#      has the old module path, which breaks the context of the next patch),
#      run `git am --continue`, then rerun this script with --continue.
#   5. Rewrite the old module path on any line the port added (left by a
#      3-way merge) and amend it into the last commit, which keeps that
#      commit's author. Lines already in the base are not touched.
#   6. go build and go vet.
#   7. With --push: push port/<n> and open a PR labeled `port` with a body
#      from port-pr-body.md.
#
# Files the PR changes outside sdk/go are not ported and are listed in the
# PR body; that part stays in core.
set -euo pipefail

core_repo=NVIDIA/OpenShell
sdk_repo=ai-openshell/sdk-go
old_mod=github.com/NVIDIA/OpenShell/sdk/go
new_mod=github.com/ai-openshell/sdk-go

base=origin/main
core_url=https://github.com/NVIDIA/OpenShell.git
push=0
build=1
cont=0
fix=0
pr=

usage() {
  sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//' >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case $1 in
  --base) base=${2:?--base needs a ref}; shift 2 ;;
  --core) core_url=${2:?--core needs a URL}; shift 2 ;;
  --push) push=1; shift ;;
  --no-build) build=0; shift ;;
  --continue) cont=1; shift ;;
  --fix-paths) fix=1; shift ;;
  -h | --help) usage ;;
  -*) echo "ERROR: unknown option $1" >&2; usage ;;
  *) [ -z "$pr" ] || usage; pr=$1; shift ;;
  esac
done
case $pr in '' | *[!0-9]*) usage ;; esac

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
body_tmpl="$here/port-pr-body.md"
branch="port/$pr"
src="refs/port-src/$pr"
state_dir="$(git rev-parse --git-path "port-src/$pr")"

die() { echo "ERROR: $*" >&2; exit 1; }

for tool in git gh go; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed"
done
[ -f "$body_tmpl" ] || die "$body_tmpl is missing"
[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] ||
  die "run this from the root of the sdk-go clone"

# Prints file:line for every line that `git diff <args>` adds and that still has
# the old module path. Only added lines count: the base may mention the old path
# on purpose (the README's migration section).
added_old_lines() {
  git diff -U0 --no-color --no-renames "$@" -- . ':!hack/migration/' |
    awk -v mod="$old_mod" '
      /^\+\+\+ / { file = substr($0, 7); next }
      /^@@/ { split($3, a, ","); line = substr(a[1], 2) + 0; next }
      /^\+/ { if (index($0, mod)) print file ":" line; line++ }'
}

# Rewrites the old module path on those added lines. Prints what it changed.
rewrite_added() {
  local loc f n
  added_old_lines "$@" | while IFS= read -r loc; do
    f=${loc%:*}
    n=${loc##*:}
    sed -i.bak "${n}s#${old_mod}#${new_mod}#g" "$f" && rm -f "$f.bak"
    echo "$loc"
  done
}

if [ "$fix" -eq 1 ]; then
  [ "$(git branch --show-current)" = "$branch" ] || die "switch to $branch first"
  [ -z "$(git diff --name-only --diff-filter=U)" ] ||
    die "resolve the conflicts first: $(git diff --name-only --diff-filter=U | tr '\n' ' ')"
  fixed=$(rewrite_added HEAD)
  if [ -z "$fixed" ]; then
    echo "No old module path on changed lines."
  else
    echo "Rewrote the old module path on:"
    sed 's/^/  /' <<<"$fixed"
    git add -- $(sed 's/:[0-9]*$//' <<<"$fixed" | sort -u)
  fi
  exit 0
fi

if [ "$cont" -eq 0 ]; then
  git diff --quiet && git diff --cached --quiet ||
    die "the working tree has changes; commit or remove them first"
  [ ! -d "$(git rev-parse --git-path rebase-apply)" ] ||
    die "a git am is in progress; finish it or run git am --abort"
  git rev-parse --verify --quiet "refs/heads/$branch" >/dev/null &&
    die "branch $branch exists; delete it to port again"
  git rev-parse --verify --quiet "$base^{commit}" >/dev/null ||
    die "base $base is not a commit"
  [ "$(git show "$base:go.mod" | sed -n 's/^module //p')" = "$new_mod" ] ||
    die "$base is not the standalone module (go.mod module is not $new_mod)"

  meta=$(gh pr view "$pr" -R "$core_repo" \
    --json state,baseRefName,headRefOid,title,url,author)
  state=$(jq -r .state <<<"$meta")
  [ "$state" = OPEN ] || die "$core_repo#$pr is $state, only open PRs are ported"
  core_base=$(jq -r .baseRefName <<<"$meta")
  head_oid=$(jq -r .headRefOid <<<"$meta")

  echo "Fetching $core_repo#$pr ($head_oid) and $core_base ..."
  git fetch --quiet --no-tags "$core_url" \
    "+refs/pull/$pr/head:$src/head" "+refs/heads/$core_base:$src/base"
  [ "$(git rev-parse "$src/head")" = "$head_oid" ] ||
    die "fetched head does not match $head_oid; the PR changed during the fetch, rerun"
  mb=$(git merge-base "$src/base" "$src/head")

  rm -rf "$state_dir"
  mkdir -p "$state_dir/patches"
  jq . <<<"$meta" >"$state_dir/meta.json"
  echo "$mb" >"$state_dir/merge-base"
  git diff --name-only "$mb" "$src/head" -- . ':!sdk/go/' >"$state_dir/outside.txt"

  # --no-signoff: a format.signoff=true config would add the porter's sign-off
  # to every commit; only the original trailers are kept.
  git format-patch --quiet --no-signoff --relative=sdk/go -o "$state_dir/patches" \
    "$mb..$src/head" -- sdk/go
  set -- "$state_dir"/patches/*.patch
  [ -e "$1" ] || die "$core_repo#$pr has no commits that touch sdk/go"
  sed -i.bak "s#${old_mod}#${new_mod}#g" "$@" && rm -f "$state_dir"/patches/*.bak
  echo "Patches: $# (merge base $(git rev-parse --short "$mb"))"
  if [ -s "$state_dir/outside.txt" ]; then
    echo "Not ported (outside sdk/go):"
    sed 's/^/  /' "$state_dir/outside.txt"
  fi

  git switch --quiet -c "$branch" "$base"
  echo "$(git rev-parse HEAD)" >"$state_dir/start"
  if ! git am --quiet --3way "$@"; then
    again="$0 --continue"
    [ "$base" = origin/main ] || again="$again --base $base"
    [ "$push" -eq 0 ] || again="$again --push"
    cat >&2 <<EOF

git am stopped on a conflict. Resolve it, then:
  git add <files>
  $0 --fix-paths $pr
  git am --continue
  $again $pr
To give up: git am --abort && git switch - && git branch -D $branch
EOF
    exit 1
  fi
else
  [ -f "$state_dir/meta.json" ] || die "no port of #$pr is in progress"
  [ "$(git branch --show-current)" = "$branch" ] || die "switch to $branch first"
  [ ! -d "$(git rev-parse --git-path rebase-apply)" ] ||
    die "git am is still in progress; run git am --continue first"
  meta=$(cat "$state_dir/meta.json")
fi

start=$(cat "$state_dir/start")
ncommits=$(git rev-list --count "$start..HEAD")
[ "$ncommits" -gt 0 ] || die "no commits were applied"

left=$(rewrite_added "$start" HEAD)
if [ -n "$left" ]; then
  echo "Rewrote the old module path on added lines (left by a 3-way merge):"
  sed 's/^/  /' <<<"$left"
  git add -u
  git commit --quiet --amend --no-edit
fi
[ -z "$(added_old_lines "$start" HEAD)" ] ||
  die "added lines still use $old_mod: $(added_old_lines "$start" HEAD | tr '\n' ' ')"

if [ "$build" -eq 1 ]; then
  echo "Building ..."
  go build ./... && go vet ./... || die "build or vet failed on $branch; fix it with a new commit, then rerun with --continue"
fi

# Render the PR body.
title=$(jq -r .title <<<"$meta")
url=$(jq -r .url <<<"$meta")
author=$(jq -r .author.login <<<"$meta")
head_oid=$(jq -r .headRefOid <<<"$meta")
freeze=$(git show "$start:hack/migration/FREEZE" 2>/dev/null | sed -n 's/^sha=//p')
commits=$(git log --reverse --format='- %h %s (%an)' "$start..HEAD")
if [ -s "$state_dir/outside.txt" ]; then
  outside=$(printf 'These files are outside `sdk/go` and stay in core:\n\n'; sed 's/^/- `/; s/$/`/' "$state_dir/outside.txt")
else
  outside='The PR changed only `sdk/go`, so nothing stays in core.'
fi
body="$state_dir/body.md"
CORE_PR="$core_repo#$pr" CORE_URL="$url" AUTHOR="@$author" HEAD_OID="$head_oid" \
FREEZE_SHA="${freeze:-unknown}" COMMITS="$commits" OUTSIDE="$outside" \
  awk '{
    while (match($0, /\{\{[A-Z_]+\}\}/)) {
      key = substr($0, RSTART + 2, RLENGTH - 4)
      $0 = substr($0, 1, RSTART - 1) ENVIRON[key] substr($0, RSTART + RLENGTH)
    }
    print
  }' "$body_tmpl" >"$body"

echo
echo "Branch $branch: $ncommits commit(s) on $(git rev-parse --short "$start")"
git log --format='  %h %an <%ae>  %s' "$start..HEAD"
echo
echo "PR title: $title"
echo "PR body ($body):"
sed 's/^/  | /' "$body"

if [ "$push" -eq 0 ]; then
  echo
  echo "Review the branch and body, then rerun with --continue --push to open the PR."
  exit 0
fi

git push --quiet -u origin "$branch"
gh pr create -R "$sdk_repo" --base main --head "$branch" \
  --title "$title" --body-file "$body" --label port
