#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# fill-template.sh: print a template with its {{KEY}} placeholders filled.
#
# Usage: fill-template.sh <template> KEY=value...
#
# Each KEY=value argument names a placeholder and its value, so the keys are
# listed exactly once. Values are inserted as is and never scanned again
# (fill-template.awk); a placeholder in the template without an argument
# stops the run with an error. Used by open-bump-pr.sh and
# hack/migration/port-pr.sh; tested by fill-template-test.sh.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }
[ $# -ge 1 ] || die "usage: $0 <template> KEY=value..."
tmpl=$1
shift
[ -f "$tmpl" ] || die "template $tmpl is missing"

keys=
for kv in "$@"; do
  key=${kv%%=*}
  [ "$key" != "$kv" ] && [[ $key =~ ^[A-Z_]+$ ]] || die "expected KEY=value with KEY in A-Z and _, got '${kv:0:40}'"
  keys+="$key "
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec env "$@" awk -v keys="$keys" -f "$here/fill-template.awk" "$tmpl"
