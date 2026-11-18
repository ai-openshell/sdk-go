# SPDX-License-Identifier: Apache-2.0

# rpc-issues-lib.sh: shared helpers for the RPC coverage issues. Sourced by
# rpc-issues.sh and rpc-issues-test.sh; defines functions only.
# shellcheck shell=bash

# rpc_bot_issues <bot-login> [file]: the issues in file (or stdin), JSON
# values with an "author" login as `gh api --paginate --jq` prints them, that
# bot-login opened, as one JSON array without the author field. Coverage
# issues are managed only when the bot opened them, so a marker in anyone
# else's issue is ignored.
rpc_bot_issues() {
  jq -s --arg bot "${1:?rpc_bot_issues needs the bot login}" 'map(select(.author == $bot) | del(.author))' ${2:+"$2"}
}

# rpc_parse_exceptions <file>: print the entries of an exceptions list
# (hack/automation/rpc-exceptions.txt) as a JSON array of {key, reason}.
# Lines whose first non-blank character is "#" and blank lines are ignored.
# Each entry is "Service.Method  reason"; a malformed key or a missing reason
# prints "ERROR: <file>:<line>: ..." on stderr and returns 1.
rpc_parse_exceptions() {
  local file=${1:?rpc_parse_exceptions needs a file} rows
  [ -f "$file" ] || { echo "ERROR: $file: no such file" >&2; return 1; }
  rows=$(awk -v f="$file" '
    { sub(/\r$/, "") }
    /^[[:space:]]*(#|$)/ { next }
    {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      key = line; sub(/[[:space:]].*$/, "", key)
      reason = substr(line, length(key) + 1)
      sub(/^[[:space:]]+/, "", reason); sub(/[[:space:]]+$/, "", reason)
      if (key !~ /^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$/) {
        printf "ERROR: %s:%d: \"%s\" is not Service.Method\n", f, NR, key > "/dev/stderr"
        bad = 1; exit 1
      }
      if (reason == "") {
        printf "ERROR: %s:%d: %s has no reason\n", f, NR, key > "/dev/stderr"
        bad = 1; exit 1
      }
      gsub(/\t/, " ", reason)
      print key "\t" reason
    }
    END { exit bad }' "$file") || return 1
  jq -Rn '[inputs | select(length > 0) | split("\t") | {key: .[0], reason: .[1]}]' <<<"$rows"
}
