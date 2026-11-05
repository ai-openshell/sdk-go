#!/usr/bin/env bash
# tag-release.sh: create the lockstep SDK release for a stable core pin.
#
# Usage: tag-release.sh [--dry-run] [--force] [--sha <commit>] [--since <commit>]
#
# Reads the pin from buf.gen.yaml at <commit> (default: $GITHUB_SHA, else
# HEAD). A pre-release pin creates nothing. Without --force, nothing is
# created either when the pin at <commit> equals the pin at --since (default:
# the parent of <commit>), so ordinary pushes to main are no-ops. The push
# workflow passes the commit before the push as --since, which covers pushes
# of several commits and rebase merges. Then, each step only when missing:
#   - tag vX.Y.Z (lightweight) at <commit>; a tag at another commit fails
#   - the GitHub release with the merged proposal's body plus
#     "Matches OpenShell core vX.Y.Z"
#   - a request to the Go module proxy for the version
# A re-run after a partial failure finishes the missing steps.
set -euo pipefail

sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
module=github.com/ai-openshell/sdk-go
dry=0
force=0
sha=${GITHUB_SHA:-}
since=

usage() { echo "usage: $0 [--dry-run] [--force] [--sha <commit>] [--since <commit>]" >&2; exit 2; }
while [ $# -gt 0 ]; do
  case $1 in
  --dry-run) dry=1; shift ;;
  --force) force=1; shift ;;
  --sha) sha=${2:?--sha needs a commit}; shift 2 ;;
  --since) since=${2:?--since needs a commit}; shift 2 ;;
  *) usage ;;
  esac
done
[ -n "$sha" ] || sha=$(git rev-parse HEAD)
sha=$(git rev-parse "$sha^{commit}")
short=$(git rev-parse --short "$sha")

pin_at() { git show "$1:buf.gen.yaml" 2>/dev/null | yq -r '.inputs[0].tag'; }

pin=$(pin_at "$sha")
if ! [[ "$pin" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Pin $pin is not a stable release; no SDK tag."
  exit 0
fi

if [ "$force" -eq 0 ]; then
  # A new branch push reports an all-zero "before"; fall back to the parent.
  if [ -z "$since" ] || ! git rev-parse --verify --quiet "$since^{commit}" >/dev/null; then
    since="$sha^"
  fi
  if [ "$(pin_at "$since" || true)" = "$pin" ]; then
    echo "The pin is $pin both at $short and at $(git rev-parse --short "$since"); no SDK tag."
    exit 0
  fi
fi

# get_or_empty <api path> <jq>: the value, empty on 404, and fail on any other
# error. gh prints the error body on stdout for a 404, so stdout is only used
# after a successful call.
get_or_empty() {
  local out err
  if out=$(gh api "$1" --jq "$2" 2>"${tmp}/err"); then
    printf '%s' "$out"
  else
    err=$(cat "${tmp}/err")
    grep -q 'HTTP 404' <<<"$err" || { echo "ERROR: gh api $1 failed: $err" >&2; return 1; }
  fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

existing=$(get_or_empty "repos/$sdk_repo/git/ref/tags/$pin" '.object.sha')
if [ -n "$existing" ] && [ "$existing" != "$sha" ]; then
  echo "ERROR: tag $pin exists at $existing, not at $sha. SDK tags never move; fix this by hand." >&2
  exit 1
fi
release=$(get_or_empty "repos/$sdk_repo/releases/tags/$pin" '.id')
if [ -n "$existing" ] && [ -n "$release" ]; then
  echo "Tag $pin and its release exist at $short already; nothing to do."
  exit 0
fi

# Release notes: the body of the PR that merged this commit, then the match line.
notes="$tmp/notes.md"
pr_body=$(get_or_empty "repos/$sdk_repo/commits/$sha/pulls" '.[0].body // empty')
{
  [ -z "$pr_body" ] || printf '%s\n\n' "$pr_body"
  echo "Matches OpenShell core $pin."
} >"$notes"

if [ "$dry" -eq 1 ]; then
  if [ -n "$existing" ]; then
    echo "dry-run: tag $pin exists at $sha"
  else
    echo "dry-run: would create tag $pin at $sha"
  fi
  echo "dry-run: would create release $pin with notes:"
  sed 's/^/  | /' "$notes"
  echo "dry-run: would request https://proxy.golang.org/$module/@v/$pin.info"
  exit 0
fi

if [ -z "$existing" ]; then
  gh api -X POST "repos/$sdk_repo/git/refs" -f ref="refs/tags/$pin" -f sha="$sha" --silent
  echo "Created tag $pin at $sha."
fi
if [ -z "$release" ]; then
  gh release create "$pin" -R "$sdk_repo" --verify-tag --title "$pin" --notes-file "$notes"
fi

# Best effort: the proxy fetches the version on first request.
if curl -fsS --max-time 60 "https://proxy.golang.org/$module/@v/$pin.info"; then
  echo
else
  echo "WARNING: the Go module proxy did not serve $pin yet; it will on the next request." >&2
fi
