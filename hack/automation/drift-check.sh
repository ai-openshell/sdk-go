#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# drift-check.sh: compare core main with the pinned core tag.
#
# Usage: drift-check.sh --out <report.md>
#
# Run from the root of an sdk-go clone. Three checks:
#   1. buf breaking: core main against the pinned tag (FILE rules), for the
#      proto files the SDK generates (the M options in buf.gen.yaml)
#   2. regenerate and build: bindings from core main in a temporary
#      worktree, then go build ./... && go vet ./...
#   3. uncovered RPCs: methods in the regenerated proto/openshellv1 gRPC
#      stubs that the SDK does not cover, decided per service by
#      rpc-coverage.sh (fakes and tests excluded); informational, it does
#      not fail the check
# Writes a markdown report with the header
# "Pin: <tag> | Core main: <sha> | Result: pass|fail" and one section per
# check. Exits 0 when checks 1 and 2 pass, 1 otherwise.
set -euo pipefail
# shellcheck source=hack/automation/rpc-coverage.sh
. "$(dirname "${BASH_SOURCE[0]}")/rpc-coverage.sh"

core_url=${CORE_URL:-https://github.com/NVIDIA/OpenShell.git}
out=
while [ $# -gt 0 ]; do
  case $1 in
  --out) out=${2:?--out needs a file}; shift 2 ;;
  *) echo "usage: $0 --out <report.md>" >&2; exit 2 ;;
  esac
done
[ -n "$out" ] || { echo "usage: $0 --out <report.md>" >&2; exit 2; }
[ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || {
  echo "ERROR: run this from the root of the sdk-go clone" >&2
  exit 1
}
case $out in /*) ;; *) out="$(pwd)/$out" ;; esac

pin=$(yq -r '.inputs[0].tag' buf.gen.yaml)
main_sha=$(git ls-remote "$core_url" refs/heads/main | cut -f1)
[ -n "$main_sha" ] || { echo "ERROR: cannot resolve core main" >&2; exit 1; }

work=$(mktemp -d)
cleanup() { git worktree remove --force "$work/wt" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
result=pass
fence() { printf '```text\n%s\n```\n' "$1"; }

# 1. buf breaking for the files the SDK generates.
files=$(yq -r '.plugins[0].opt[] | select(test("^M")) | sub("^M"; "") | sub("=.*"; "")' buf.gen.yaml)
path_args=()
for f in $files; do path_args+=(--path "proto/$f"); done
# Core's root buf.yaml has two modules; this config keeps only proto/, so
# its imports resolve the same way buf generate sees them.
if breaking=$(buf breaking "${core_url}#ref=${main_sha}" \
  --against "${core_url}#tag=${pin}" \
  --config '{"version":"v2","modules":[{"path":"proto"}],"breaking":{"use":["FILE"]}}' \
  "${path_args[@]}" 2>&1); then
  breaking_section="No breaking changes in $(echo $files | sed 's/ /, /g') between \`$pin\` and main."
else
  result=fail
  breaking_section="Breaking changes between \`$pin\` and main:

$(fence "$breaking")"
fi

# 2. Regenerate from main and build.
git worktree add --quiet --detach "$work/wt" HEAD
if build=$(
  cd "$work/wt" &&
    yq -i 'del(.inputs[0].tag) | .inputs[0].ref = "'"$main_sha"'"' buf.gen.yaml &&
    buf generate 2>&1 &&
    go build ./... 2>&1 &&
    go vet ./... 2>&1
); then
  changed=$(git -C "$work/wt" diff --stat -- proto/ | tail -1 | sed 's/^ *//')
  build_section="Bindings from main build and vet cleanly (${changed:-no generated files change})."
else
  result=fail
  build_section="Regenerating from main or building against it fails:

$(fence "$(tail -n 60 <<<"$build")")"
fi

# 3. RPCs the SDK does not call yet. Full method names look like
# "/openshell.v1.OpenShell/CreateSandbox"; coverage is decided per service by
# rpc-coverage.sh, the rule rpc-issues.sh uses too.
uncovered=""
if ls "$work/wt"/proto/openshellv1/*_grpc.pb.go >/dev/null 2>&1; then
  grep -ho '"/[a-zA-Z0-9_.]*\.[A-Za-z0-9_]*/[A-Za-z0-9_]*"' "$work/wt"/proto/openshellv1/*_grpc.pb.go |
    tr -d '"' | sed -E 's#^/([A-Za-z0-9_]+\.)*([A-Za-z0-9_]+)/([A-Za-z0-9_]+)$#\2.\3#' |
    sort -u >"$work/keys.txt"
  covered=$(rpc_covered_list . "$work/keys.txt") || {
    echo "ERROR: cannot determine the RPCs the SDK covers" >&2
    exit 1
  }
  while read -r key; do
    grep -qxF "$key" <<<"$covered" || uncovered+="- ${key}"$'\n'
  done <"$work/keys.txt"
fi
if [ -z "$uncovered" ]; then
  uncovered_section="Every RPC in the regenerated stubs is called by the SDK."
else
  uncovered_section="RPCs in core main that the SDK does not call yet ($(grep -c . <<<"$uncovered")):

${uncovered%$'\n'}"
fi

cat >"$out" <<EOF
Pin: $pin | Core main: $main_sha | Result: $result

## buf breaking

$breaking_section

## Regenerate and build

$build_section

## Uncovered RPCs

$uncovered_section
EOF

echo "Drift check against core main $main_sha: $result (report: $out)"
[ "$result" = pass ]
