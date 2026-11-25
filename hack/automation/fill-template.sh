#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# fill-template.sh: print a template with its {{KEY}} placeholders filled.
#
# Usage: fill-template.sh <template> KEY=value...
#
# Each KEY=value argument names a placeholder and its value, so the keys are
# listed exactly once. The values reach jq as --arg arguments, never through
# the environment, and are inserted as is: no escape processing, and no
# second scan for placeholders (fill in template.jq). A placeholder in the
# template without an argument stops the run with an error. Used by
# open-bump-pr.sh and hack/migration/port-pr.sh; tested by
# fill-template-test.sh.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }
[ $# -ge 1 ] || die "usage: $0 <template> KEY=value..."
tmpl=$1
shift
[ -f "$tmpl" ] || die "template $tmpl is missing"

args=()
for kv in "$@"; do
  key=${kv%%=*}
  [ "$key" != "$kv" ] && [[ $key =~ ^[A-Z_]+$ ]] || die "expected KEY=value with KEY in A-Z and _, got '${kv:0:40}'"
  args+=(--arg "$key" "${kv#*=}")
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# -R -s: the whole template as one string; -j: print it without adding a
# newline, so the output keeps the template's own line endings.
# ${args[@]+...}: bash 3.2 (macOS) treats an empty array as unset under -u.
jq -L "$here" -Rsj ${args[@]+"${args[@]}"} 'include "template"; fill($ARGS.named)' <"$tmpl" ||
  die "could not fill $tmpl"
