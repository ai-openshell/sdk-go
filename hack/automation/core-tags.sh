#!/usr/bin/env bash
# core-tags.sh: print NVIDIA/OpenShell release tags newer than the pin,
# oldest first.
#
# Usage: core-tags.sh [--all] [--pin <tag>]
#
# Release tags are vX.Y.Z and vX.Y.Z-pre.N; a pre-release sorts below its
# stable version. By default every newer stable tag is printed, plus the
# newest pre-release when it is newer than all of them: one proposal for the
# current release train instead of one per pre-release. --all prints every
# newer tag. --pin overrides the pin read from buf.gen.yaml.
set -euo pipefail

repo=${CORE_REPO:-NVIDIA/OpenShell}
all=0
pin=

while [ $# -gt 0 ]; do
  case $1 in
  --all) all=1; shift ;;
  --pin) pin=${2:?--pin needs a tag}; shift 2 ;;
  *) echo "usage: $0 [--all] [--pin <tag>]" >&2; exit 2 ;;
  esac
done

[ -n "$pin" ] || pin=$(yq -r '.inputs[0].tag' buf.gen.yaml)

# key <tag>: "major minor patch rank pre" with rank 1 for stable, 0 for pre.
key() {
  awk -v t="$1" 'BEGIN {
    s = t; sub(/^v/, "", s)
    pre = ""; rank = 1
    if (index(s, "-pre.")) { pre = substr(s, index(s, "-pre.") + 5); s = substr(s, 1, index(s, "-pre.") - 1); rank = 0 }
    split(s, v, ".")
    printf "%d %d %d %d %d\n", v[1], v[2], v[3], rank, (pre == "" ? 0 : pre)
  }'
}

newer=$(
  gh api --paginate "repos/$repo/git/matching-refs/tags/v" --jq '.[].ref' |
    sed 's#^refs/tags/##' |
    { grep -E '^v[0-9]+\.[0-9]+\.[0-9]+(-pre\.[0-9]+)?$' || true; } |
    while read -r t; do echo "$(key "$t") $t"; done |
    awk -v p="$(key "$pin")" '
      BEGIN { split(p, k, " ") }
      {
        for (i = 1; i <= 5; i++) {
          if ($i + 0 > k[i] + 0) { print; next }
          if ($i + 0 < k[i] + 0) next
        }
      }' |
    sort -n -k1,1 -k2,2 -k3,3 -k4,4 -k5,5 |
    awk '{ print $6 }'
)

[ -n "$newer" ] || exit 0
if [ "$all" -eq 1 ]; then
  echo "$newer"
  exit 0
fi

# Stable tags, then the last tag if it is a pre-release (newer than every stable).
grep -v -- '-pre\.' <<<"$newer" || true
last=$(tail -1 <<<"$newer")
case $last in *-pre.*) echo "$last" ;; esac
