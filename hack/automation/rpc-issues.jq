# SPDX-License-Identifier: Apache-2.0
#
# rpc-issues.jq: the planner for RPC coverage issues. Pure: one input
# document in, one action list out; rpc-issues.sh gathers the input and
# applies the actions, rpc-issues-test.sh runs this on fixtures.
#
#   jq -f rpc-issues.jq --rawfile template rpc-issue-body.md input.json
#
# Input (see specs data-model): repo, core_repo, tag, max_new, rpcs (one row
# per method of every non-google proto file), services_bound, covered, built,
# generated_files, bump_branch_exists, exceptions, issues.

# Per-RPC flags.
def rpc_rows:
  . as $in
  | ([$in.rpcs[] | select(.auth_mode != null) | .file] | unique) as $served
  | ([$in.exceptions[]? | {key: .key, value: .reason}] | from_entries) as $exc
  | ([$in.covered[]? | {key: ., value: true}] | from_entries) as $cov
  | ([$in.built[]? | {key: ., value: true}] | from_entries) as $built
  | ($in.generated_files // []) as $gen
  | [$in.rpcs[]
     | (.service + "." + .method) as $key
     | . + {
         key: $key,
         user_facing: ((.file | IN($served[])) and (.auth_mode == null or .auth_mode == "bearer" or .auth_mode == "dual")),
         unannotated: (.auth_mode == null),
         excepted: ($exc | has($key)),
         exception_reason: $exc[$key],
         is_covered: ($cov | has($key)),
         buildability: (
           if $built | has($key) then "built"
           elif (.file | IN($gen[]) | not) then "needs-bindings"
           elif $in.bump_branch_exists then "bump-branch"
           else "bump-pending" end)
       }
     | .applicable = (.user_facing and (.is_covered | not) and (.excepted | not))];

rpc_rows as $rows
| []
