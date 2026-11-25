# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash

# pin-bump.sh: the edits a core bump makes besides the regenerated bindings.
# Sourced, not run. open-bump-pr.sh applies them; merge-bump-pr.sh rebuilds
# them to check that a proposal is exactly a pin bump and nothing else.

# pin_bump_edit <tag> <kind>: in the current directory, set the pin in
# buf.gen.yaml to <tag> and, for a stable <kind>, keep the README's
# "generated from OpenShell core vX.Y.Z" line in step. The README line may
# still name the last stable tag after a merged pre-release proposal, so any
# version matches.
pin_bump_edit() {
  local tag=$1 kind=$2
  yq -i ".inputs[0].tag = \"$tag\"" buf.gen.yaml
  if [ "$kind" = stable ]; then
    sed -E -i.bak "s/generated from OpenShell core v[0-9]+\.[0-9]+\.[0-9]+(-pre\.[0-9]+)?/generated from OpenShell core ${tag}/" README.md
    rm -f README.md.bak
  fi
}

# pin_bump_tree <base> <tag> <kind>: print the tree a bump commit on <base>
# must have when it changes nothing but the pin edits: <base>'s tree with
# pin_bump_edit applied to buf.gen.yaml and README.md. No worktree, no
# checkout, no generated code; the blobs are written to the object store.
pin_bump_tree() (
  set -euo pipefail
  base=$1 tag=$2 kind=$3
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  for f in buf.gen.yaml README.md; do
    git show "$base:$f" >"$tmp/$f"
  done
  (cd "$tmp" && pin_bump_edit "$tag" "$kind")
  # A private index, so the caller's index and worktree stay untouched.
  export GIT_INDEX_FILE="$tmp/index"
  git read-tree "$base"
  for f in buf.gen.yaml README.md; do
    mode=$(git ls-tree "$base" -- "$f" | cut -d' ' -f1)
    blob=$(git hash-object -w "$tmp/$f")
    git update-index --cacheinfo "$mode,$blob,$f"
  done
  git write-tree
)
