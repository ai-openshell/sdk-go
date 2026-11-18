# SPDX-License-Identifier: Apache-2.0

# rpc-coverage.sh: the SDK's definition of a covered core RPC. Sourced by
# rpc-issues.sh, drift-check.sh, and rpc-issues-test.sh; defines functions
# only.
#
# Only non-test Go files under <sdk-root>/openshell/v1 outside fake/ count.
# A package directory binds service S when one of its files calls
# New<S>Client( (the generated constructor). Method M of S is covered when a
# file in a package that binds S contains ".M(". Methods with the same name
# in different services are told apart by the binding.
# shellcheck shell=bash

# _rpc_go_files <sdk-root>: NUL-separated SDK Go files that count.
_rpc_go_files() {
  local v1="$1/openshell/v1"
  [ -d "$v1" ] || { echo "ERROR: $v1 does not exist" >&2; return 1; }
  find "$v1" -type f -name '*.go' ! -name '*_test.go' \
    ! -path "$v1/fake/*" ! -path "$v1/*/fake/*" -print0
}

# _rpc_bindings <sdk-root>: "<package-dir> <service>" lines, sorted; returns 1
# when no package binds any service.
_rpc_bindings() {
  local out
  out=$(_rpc_go_files "$1" |
    xargs -0 grep -HoE '(^|[^A-Za-z0-9_])New[A-Za-z0-9_]+Client\(' /dev/null |
    awk '{
      i = index($0, ":"); path = substr($0, 1, i - 1); m = substr($0, i + 1)
      sub(/^[^A-Za-z0-9_]?New/, "", m); sub(/Client\($/, "", m)
      dir = path; sub(/\/[^\/]*$/, "", dir)
      print dir, m
    }' | sort -u) || true
  [ -n "$out" ] || { echo "ERROR: no package under $1/openshell/v1 binds a service (no New<Service>Client( call)" >&2; return 1; }
  echo "$out"
}

# rpc_bound_services <sdk-root>: the services some package binds, sorted.
rpc_bound_services() {
  local b
  b=$(_rpc_bindings "$1") || return 1
  awk '{ print $2 }' <<<"$b" | sort -u
}

# rpc_covered_list <sdk-root> <keys-file>: the Service.Method lines of
# keys-file that the SDK covers, in input order.
rpc_covered_list() {
  local root=$1 keys=${2:?rpc_covered_list needs a keys file} b calls
  [ -f "$keys" ] || { echo "ERROR: $keys: no such file" >&2; return 1; }
  b=$(_rpc_bindings "$root") || return 1
  calls=$(_rpc_go_files "$root" |
    xargs -0 grep -HoE '\.[A-Za-z0-9_]+\(' /dev/null |
    awk '{
      i = index($0, ":"); path = substr($0, 1, i - 1); m = substr($0, i + 2)
      sub(/\($/, "", m)
      dir = path; sub(/\/[^\/]*$/, "", dir)
      print dir, m
    }' | sort -u) || true
  # Three inputs: bindings, calls, keys.
  awk '
    FILENAME == ARGV[1] { bound[$1] = bound[$1] " " $2; next }
    FILENAME == ARGV[2] {
      if (!($1 in bound)) next
      m = split(bound[$1], svc, " ")
      for (j = 1; j <= m; j++) covered[svc[j] "." $2] = 1
      next
    }
    { sub(/\r$/, "") }
    $0 in covered { print }' <(printf '%s\n' "$b") <(printf '%s\n' "$calls") "$keys"
}
