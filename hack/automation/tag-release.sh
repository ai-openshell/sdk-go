#!/usr/bin/env bash
# tag-release.sh: create the lockstep SDK release for a stable core pin.
#
# Usage: tag-release.sh [--dry-run] [--force] [--sha <commit>]
#
# Reads the pin from buf.gen.yaml at <commit> (default: $GITHUB_SHA, else
# HEAD). A pre-release pin creates nothing. Without --force, a commit that
# did not change buf.gen.yaml creates nothing either, so ordinary pushes to
# main are no-ops. Then:
#   - tag vX.Y.Z missing: create it (lightweight) at <commit>, create the
#     GitHub release with the merged proposal's body plus
#     "Matches OpenShell core vX.Y.Z", and ask the Go module proxy for it
#   - tag at <commit> already: nothing to do
#   - tag at another commit: fail
set -euo pipefail

sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
module=github.com/ai-openshell/sdk-go
dry=0
force=0
sha=${GITHUB_SHA:-}

while [ $# -gt 0 ]; do
  case $1 in
  --dry-run) dry=1; shift ;;
  --force) force=1; shift ;;
  --sha) sha=${2:?--sha needs a commit}; shift 2 ;;
  *) echo "usage: $0 [--dry-run] [--force] [--sha <commit>]" >&2; exit 2 ;;
  esac
done
[ -n "$sha" ] || sha=$(git rev-parse HEAD)
sha=$(git rev-parse "$sha^{commit}")

pin=$(git show "$sha:buf.gen.yaml" | yq -r '.inputs[0].tag')
if ! [[ "$pin" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Pin $pin is not a stable release; no SDK tag."
  exit 0
fi

if [ "$force" -eq 0 ] && [ -z "$(git diff --name-only "$sha^" "$sha" -- buf.gen.yaml 2>/dev/null)" ]; then
  echo "$(git rev-parse --short "$sha") does not change buf.gen.yaml; no SDK tag."
  exit 0
fi

# gh prints the error body on stdout for a 404, so only trust a successful call.
if ! existing=$(gh api "repos/$sdk_repo/git/ref/tags/$pin" --jq '.object.sha' 2>/dev/null); then
  existing=
fi
if [ -n "$existing" ]; then
  if [ "$existing" = "$sha" ]; then
    echo "Tag $pin already points at $(git rev-parse --short "$sha"); nothing to do."
    exit 0
  fi
  echo "ERROR: tag $pin exists at $existing, not at $sha. SDK tags never move; fix this by hand." >&2
  exit 1
fi

# Release notes: the body of the PR that merged this commit, then the match line.
notes=$(mktemp)
trap 'rm -f "$notes"' EXIT
if ! pr_body=$(gh api "repos/$sdk_repo/commits/$sha/pulls" --jq '.[0].body // empty' 2>/dev/null); then
  pr_body=
fi
{
  [ -z "$pr_body" ] || printf '%s\n\n' "$pr_body"
  echo "Matches OpenShell core $pin."
} >"$notes"

if [ "$dry" -eq 1 ]; then
  echo "dry-run: would create tag $pin at $sha"
  echo "dry-run: would create release $pin with notes:"
  sed 's/^/  | /' "$notes"
  echo "dry-run: would request https://proxy.golang.org/$module/@v/$pin.info"
  exit 0
fi

gh api -X POST "repos/$sdk_repo/git/refs" -f ref="refs/tags/$pin" -f sha="$sha" --silent
echo "Created tag $pin at $sha."
gh release create "$pin" -R "$sdk_repo" --verify-tag --title "$pin" --notes-file "$notes"

# Best effort: the proxy fetches the version on first request.
if curl -fsS --max-time 60 "https://proxy.golang.org/$module/@v/$pin.info"; then
  echo
else
  echo "WARNING: the Go module proxy did not serve $pin yet; it will on the next request." >&2
fi
