#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# fill-template-test.sh: offline tests for fill-template.sh, the renderer of
# the bump and port PR bodies.
#
# Usage: fill-template-test.sh
#
# Run from the root of an sdk-go clone; part of workflows:check. Checks that
# placeholder text inside a value stays literal (no environment variable such
# as GH_TOKEN leaks into the body, and a value naming its own placeholder
# does not loop), that several placeholders on one line are all filled, that
# an unknown placeholder fails, and that the real templates render without
# leftover placeholders. Exits 1 on any failure.
set -euo pipefail

fill=hack/automation/fill-template.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail=0
ok() { echo "ok $1"; }
bad() { echo "FAIL $1: $2"; fail=1; }

# A reintroduced rescanning loop must fail the test, not hang it.
run() {
  if command -v timeout >/dev/null; then
    timeout 10 "$fill" "$@"
  else
    perl -e 'alarm 10; exec @ARGV or die "exec: $!"' "$fill" "$@" # macOS has no timeout
  fi
}

# Placeholder text in a value is inserted literally, even for a variable
# that is set in the environment and for the value's own placeholder.
printf 'a {{PROTO_DIFF}} b\n' >"$tmp/one.md"
diff=$'+  // {{GH_TOKEN}} {{NEW}}\n+  // {{PROTO_DIFF}}'
if out=$(GH_TOKEN=ghs_SECRET run "$tmp/one.md" PROTO_DIFF="$diff" NEW=v9.9.9); then
  want=$'a +  // {{GH_TOKEN}} {{NEW}}\n+  // {{PROTO_DIFF}} b'
  if [ "$out" = "$want" ]; then ok literal-values; else bad literal-values "got: $out"; fi
  case $out in *ghs_SECRET* | *v9.9.9*) bad no-leak "a value was expanded: $out" ;; *) ok no-leak ;; esac
else
  bad literal-values "exit $?"
fi

# Several placeholders on one line, special characters in values, and lines
# without placeholders.
printf '{{OLD}}...{{NEW}} {{OLD}}\nplain {line} {{lower}}\n' >"$tmp/two.md"
out=$(run "$tmp/two.md" OLD='v1 & \1' NEW='v2')
want=$'v1 & \\1...v2 v1 & \\1\nplain {line} {{lower}}'
if [ "$out" = "$want" ]; then ok several-placeholders; else bad several-placeholders "got: $out"; fi

# A placeholder without a KEY=value argument stops the run.
printf 'x {{NEW}} {{GH_TOKEN}}\n' >"$tmp/bad.md"
if GH_TOKEN=ghs_SECRET run "$tmp/bad.md" NEW=v1 >"$tmp/out" 2>"$tmp/err"; then
  bad unknown-placeholder "accepted {{GH_TOKEN}}"
elif grep -q 'unknown placeholder {{GH_TOKEN}}' "$tmp/err"; then
  ok unknown-placeholder
else
  bad unknown-placeholder "stderr: $(cat "$tmp/err")"
fi

# Malformed arguments are refused before anything is rendered, by the
# argument check rather than by a later unknown placeholder.
if run "$tmp/one.md" 'proto_diff=x' >"$tmp/out" 2>"$tmp/err"; then
  bad bad-argument "accepted a lowercase key"
elif grep -q 'expected KEY=value' "$tmp/err"; then
  ok bad-argument
else
  bad bad-argument "stderr: $(cat "$tmp/err")"
fi

# Each caller passes exactly the keys its template uses. The keys are read
# from the caller's fill-template.sh call, so a key dropped from or renamed
# in a caller fails here instead of in the scheduled automation.
caller_keys() { # <script>: the KEY= names of its fill-template.sh call
  awk '/fill-template\.sh" "\$body_tmpl"/ { on = 1 } on { print } on && />"\$body"$/ { exit }' "$1" |
    { grep -oE '(^|[[:space:]])[A-Z_]+=' || true; } | tr -d ' =\t' | sort -u # no call: empty, reported below
}
template_keys() { grep -oE '\{\{[A-Z_]+\}\}' "$1" | tr -d '{}' | sort -u; }
check_caller() { # <name> <script> <template>
  local keys args=() k out
  keys=$(caller_keys "$2")
  if [ -z "$keys" ]; then bad "$1" "no fill-template.sh call found in $2"; return; fi
  if [ "$keys" != "$(template_keys "$3")" ]; then
    bad "$1" "$2 passes [$(tr '\n' ' ' <<<"$keys")], $3 uses [$(template_keys "$3" | tr '\n' ' ')]"
    return
  fi
  for k in $keys; do args+=("$k=value-of-$k"); done
  if out=$(run "$3" "${args[@]}") && ! grep -q '{{' <<<"$out"; then ok "$1"; else bad "$1" "render failed or placeholders left"; fi
}
check_caller bump-pr-body hack/automation/open-bump-pr.sh hack/automation/bump-pr-body.md
check_caller port-pr-body hack/migration/port-pr.sh hack/migration/port-pr-body.md

[ "$fail" -eq 0 ] && echo "fill-template tests passed"
exit "$fail"
