# SPDX-License-Identifier: Apache-2.0
#
# rpc-issues.jq: the planner for RPC coverage issues. Pure: one input
# document in, one action list out; rpc-issues.sh gathers the input and
# applies the actions, rpc-issues-test.sh runs this on fixtures.
#
#   jq -f rpc-issues.jq --rawfile template rpc-issue-body.md input.json
#   jq -f rpc-issues.jq --rawfile template rpc-issue-body.md --arg mode needs-actors input.json
#
# Input: repo, core_repo, tag, max_new, rpcs (one row per method of every
# non-google proto file), services_bound, covered, built, generated_files,
# bump_branch_exists, exceptions, issues (number, state, title, body, labels,
# last_actor_is_bot, last_event).
#
# Output: actions sorted by group id, each one of
#   {action: "open", group, title, body, labels}
#   {action: "update", number, group, body?, add_labels?, remove_labels?}
#   {action: "close", number, group, comment}
#   {action: "reopen", number, group, body, comment, add_labels?, remove_labels?}
# or {error: "too-many-new", planned: [titles]} when more than max_new issues
# would be opened. With mode needs-actors, the sorted numbers of the issues
# whose last close or reopen actor the plan depends on.

def base_labels: ["help wanted", "rpc-coverage"];
def readiness_labels: ["rpc:blocked", "rpc:ready"];
def ident: test("^[A-Za-z0-9_]+$");

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

# Marker on the first body line:
#   <!-- rpc-coverage: kind=service service=S first-seen=T methods=A,B -->
#   <!-- rpc-coverage: kind=method service=S method=M first-seen=T methods=M -->
# Returns null when the line is not a valid marker.
def parse_marker:
  ((. // "") | split("\n")[0] | sub("\r$"; "")) as $line
  | [$line | capture("^<!-- rpc-coverage: (?<f>.*) -->$")] | .[0]
  | if . == null then null else
      ([.f | splits(" +") | select(length > 0) | capture("^(?<k>[a-z-]+)=(?<v>.*)$") | {key: .k, value: .v}]
       | from_entries) as $kv
      | ($kv.service // "") as $s
      | (($kv.methods // "") | split(",") | map(select(length > 0 and ident)) | unique) as $ms
      | if $kv.kind == "service" and ($s | ident) then
          {kind: "service", service: $s, id: ("service:" + $s), first_seen: $kv["first-seen"], methods: $ms}
        elif $kv.kind == "method" and ($s | ident) and (($kv.method // "") | ident) then
          {kind: "method", service: $s, method: $kv.method, id: ("method:" + $s + "." + $kv.method),
           first_seen: $kv["first-seen"], methods: [$kv.method]}
        else null end
    end;

def render_marker:
  if .kind == "service" then
    "<!-- rpc-coverage: kind=service service=\(.service) first-seen=\(.first_seen) methods=\(.methods | join(",")) -->"
  else
    "<!-- rpc-coverage: kind=method service=\(.service) method=\(.method) first-seen=\(.first_seen) methods=\(.methods | join(",")) -->"
  end;

def title:
  if .kind == "service" then "feat(sdk): wrap the \(.service) service (core \(.first_seen))"
  else "feat(sdk): wrap \(.service).\(.method) (core \(.first_seen))" end;

# "open", "bot-closed", or "human-closed". An unknown closer (not fetched)
# counts as a human, so nothing is reopened or recreated on missing data.
def issue_status:
  if .state == "OPEN" then "open"
  elif .last_actor_is_bot == true then "bot-closed"
  else "human-closed" end;

# Replace {{KEY}} placeholders of a template with the values of an object.
def fill($vals):
  reduce ($vals | to_entries[]) as $e (.; split("{{" + $e.key + "}}") | join($e.value));

def normalize: gsub("\r"; "") | sub("\\s+$"; "");

# Group buildability: the least ready state of its methods.
def group_buildability:
  map(.buildability) as $b
  | if all($b[]; . == "built") then "built"
    elif any($b[]; . == "needs-bindings") then "needs-bindings"
    elif any($b[]; . == "bump-pending") then "bump-pending"
    else "bump-branch" end;

def readiness: if group_buildability == "built" then "rpc:ready" else "rpc:blocked" end;

# Label changes for an issue: add the missing base and readiness labels,
# remove only a wrong readiness label. fields holds add_labels and
# remove_labels when they are not empty.
def label_changes($current; $ready):
  (base_labels + [$ready]) as $want
  | ([$want[] | select(IN($current[]) | not)] | sort) as $add
  | ([$current[] | select(IN(readiness_labels[])) | select(. != $ready)] | unique) as $remove
  | {add: $add, remove: $remove,
     fields: ((if ($add | length) > 0 then {add_labels: $add} else {} end)
              + (if ($remove | length) > 0 then {remove_labels: $remove} else {} end))};

def code_list: map("`" + . + "`") | if length <= 2 then join(" and ") else (.[:-1] | join(", ")) + ", and " + .[-1] end;

def streaming_kind:
  if .client_streaming and .server_streaming then "bidirectional streaming"
  elif .client_streaming then "client streaming"
  elif .server_streaming then "server streaming"
  else "unary" end;

def auth_text:
  if .auth_mode == null then
    "no authorization annotation. The gateway treats unannotated methods as user-callable, so this issue includes it; core should add the annotation."
  else
    "`\(.auth_mode)`"
    + (if .scope then ", scope `\(.scope)`" else "" end)
    + (if .workspace_role then ", workspace role `\(.workspace_role)`" else "" end)
    + (if .global_role then ", global role `\(.global_role)`" else "" end)
  end;

def proto_block: "```proto\n" + . + "\n```";

def link_list: map("[`\(.path)`](\(.url))") | join(", ");

# One section per method: signature, comment, streaming kind, auth, messages.
def method_section($tag):
  "## `\(.key)`\n\n"
  + (if (.comment // "") != "" then .comment + "\n\n" else "" end)
  + "- Streaming: \(streaming_kind)\n"
  + "- Authorization: \(auth_text)\n"
  + "- Definition: [`proto/\(.file)`](\(.proto_url)) at core `\($tag)`\n"
  + "- Generated bindings on the SDK's `main`: \(if .buildability == "built" then "yes" else "no" end)\n\n"
  + (.signature | proto_block) + "\n\n"
  + "Request and response messages:\n\n"
  + ([.messages[] | if (.source // "") != "" then .source | proto_block
                    else "`\(.name)` (definition not in core's `proto/`, for example a well-known type)" end]
     | join("\n\n"));

def refs_section:
  "### `\(.key)`\n\n"
  + "- Server (`crates/openshell-server/src`): "
  + (if (.server_refs | length) > 0 then (.server_refs | link_list) else "not found" end) + "\n"
  + "- Rust SDK (`crates/openshell-sdk/src`): "
  + (if (.rust_sdk_refs | length) > 0 then "found in " + (.rust_sdk_refs | link_list) else "not found" end) + "\n"
  + "- TypeScript SDK (`sdk/typescript/src`): "
  + (if (.ts_sdk_refs | length) > 0 then "found in " + (.ts_sdk_refs | link_list) else "not found" end);

def buildability_text($ctx):
  ($ctx.rows | group_buildability) as $b
  | "https://github.com/\($ctx.repo)/tree/core/\($ctx.tag)" as $branch_url
  | if $b == "built" then
      "**Buildable on `main`**: the generated bindings on the SDK's `main` already contain every method listed here."
    elif $b == "bump-branch" then
      "**Blocked on the core bump**: the generated bindings on the SDK's `main` do not contain every method listed here yet. Work on the bump branch [`core/\($ctx.tag)`](\($branch_url)), or on `main` once that proposal is merged."
    elif $b == "bump-pending" then
      "**Blocked on the core bump**: the generated bindings on the SDK's `main` do not contain every method listed here yet, and the bump proposal for core `\($ctx.tag)` (branch `core/\($ctx.tag)`) does not exist yet. core-watch opens it; work on that branch once it exists."
    else
      "**Blocked on bindings generation**: the SDK does not generate bindings for "
      + ([$ctx.rows[] | select(.buildability == "needs-bindings") | "proto/" + .file] | unique | code_list)
      + " yet. Add the `M` option for the file to both plugins in `buf.gen.yaml` and run `mise run proto:gen` first."
    end;

# The implementation brief: plain text, copied as the task description.
def brief($ctx):
  $ctx.group as $g
  | ($ctx.rows | group_buildability) as $b
  | [$ctx.rows[].method] as $methods
  | (if $g.kind == "service" then
       "Wrap the \($g.service) service of OpenShell core in the OpenShell Go SDK (https://github.com/\($ctx.repo)): \(if ($methods | length) == 1 then "method" else "methods" end) \($methods | join(", "))."
     else
       "Wrap the \($g.service).\($g.method) RPC of OpenShell core in the OpenShell Go SDK (https://github.com/\($ctx.repo))."
     end)
  + "\n\nCore version: \($ctx.core_repo) at tag \($ctx.tag).\n"
  + (if $b == "built" then
       "Branch: start from main; its generated bindings already contain the methods."
     elif $b == "bump-branch" then
       "Branch: start from core/\($ctx.tag), the core bump proposal for \($ctx.tag); the bindings on main do not contain the methods yet."
     elif $b == "bump-pending" then
       "Branch: core/\($ctx.tag), the core bump proposal for \($ctx.tag), which core-watch has not opened yet; start once it exists."
     else
       "Branch: start from main. First add the M option for "
       + ([$ctx.rows[] | select(.buildability == "needs-bindings") | "proto/" + .file] | unique | join(" and "))
       + " to both plugins in buf.gen.yaml and run `mise run proto:gen`."
     end)
  + "\n\nRead first:\n"
  + "- AGENTS.md and CONTRIBUTING.md in the SDK repository: conventions, commands, and commit rules.\n"
  + ([$ctx.rows[]
      | "- \(.key): proto definition \(.proto_url)"
        + (if (.server_refs | length) > 0 then "; core server: " + ([.server_refs[].url] | join(", ")) else "; core server: not found" end)
        + (if (.rust_sdk_refs | length) > 0 then "; core Rust SDK: " + ([.rust_sdk_refs[].url] | join(", ")) else "" end)
        + (if (.ts_sdk_refs | length) > 0 then "; core TypeScript SDK: " + ([.ts_sdk_refs[].url] | join(", ")) else "" end)]
     | join("\n")) + "\n"
  + "- The pattern to follow: the sub-client in openshell/v1/service_client.go, its types in openshell/v1/types/service.go, its converter in openshell/v1/internal/converter/service.go, and its fake in openshell/v1/fake/service.go.\n"
  + "\nDeliverables:\n"
  + (if $g.kind == "service" then
       "- A sub-client in openshell/v1 for the \($g.service) service, behind an interface like the other sub-clients, that constructs pb.New\($g.service)Client(conn) and is reachable from Client.\n"
     else
       "- The method in openshell/v1, on the sub-client that fits it, behind its interface like the other sub-client methods.\n"
     end)
  + "- Domain types in openshell/v1/types/ without proto imports.\n"
  + "- Converters in openshell/v1/internal/converter/ that deep-copy slices and maps.\n"
  + "- The fake implementation in openshell/v1/fake/.\n"
  + "- Unit tests with bufconn and testify.\n"
  + "- Documentation: a page under docs/src/api/ (like docs/src/api/services.md) with its entry in docs/src/SUMMARY.md, or an update of the existing page.\n"
  + "\nDone when `mise run ci` passes. Use Conventional Commits and sign off every commit.";

# Body of a group; $ctx carries the input document and the group's rows.
def render_body($ctx):
  $ctx.group as $g
  | ($g + {methods: [$ctx.rows[].method]}) as $m
  | $ctx.template
  | fill({
      MARKER: ($m | render_marker),
      SUMMARY: (
        (if $g.kind == "service" then
          "Core `\($ctx.tag)` has "
          + (if ($ctx.rows | length) == 1 then "a user-facing method" else "\($ctx.rows | length) user-facing methods" end)
          + " in the `\($g.service)` service that the Go SDK does not wrap yet: \($m.methods | code_list). "
          + "The SDK wraps none of the service's user-facing methods, so this issue tracks the service as a whole."
        else
          "Core `\($ctx.tag)` has `\($g.service).\($g.method)`, a user-facing RPC that the Go SDK does not wrap yet."
        end)
        + " First seen in core `\($g.first_seen)`."),
      BUILDABILITY: buildability_text($ctx),
      METHODS: ([$ctx.rows[] | method_section($ctx.tag)] | join("\n\n")),
      REFS: ([$ctx.rows[] | refs_section] | join("\n\n")),
      BRIEF: brief($ctx),
      EXCEPTIONS_URL: "https://github.com/\($ctx.repo)/blob/main/hack/automation/rpc-exceptions.txt"
    })
  | normalize;

def plan:
  . as $in
  | ($ARGS.named.template // error("pass the body template with --rawfile template rpc-issue-body.md")) as $template
  | rpc_rows as $rows
  | ([$rows[] | {key: .key, value: .}] | from_entries) as $by_key
  | ($in.services_bound // []) as $bound

  # Existing issues with a valid marker; one per identity: the lowest open
  # number, else the lowest number.
  | ([$in.issues[]?
      | . + {marker: (.body | parse_marker), labels: (.labels // [])}
      | select(.marker != null)
      | .status = issue_status]
     | group_by(.marker.id)
     | map(sort_by((if .state == "OPEN" then 0 else 1 end), .number) | .[0])) as $issues
  | ([$issues[] | {key: .marker.id, value: .}] | from_entries) as $by_id

  # Rule 0: open and bot-closed issues own the methods their marker lists
  # (open issues first, then by number); human-closed issues suppress.
  | (reduce ($issues | map(select(.status != "human-closed"))
             | sort_by((if .status == "open" then 0 else 1 end), .number))[] as $i
       ({}; reduce ($i.marker.methods[] | $i.marker.service + "." + .) as $k
              (.; if has($k) then . else .[$k] = $i.marker.id end))) as $owner
  | ([$issues[] | select(.status == "human-closed")]) as $human_closed
  | def suppressed($r):
      any($human_closed[]; (.marker.kind == "service" and .marker.service == $r.service)
                           or .marker.id == ("method:" + $r.key));
    ([$rows[] | select(.applicable)]) as $applicable
  | ([$applicable[] | select(.key as $k | $owner | has($k) | not) | select(suppressed(.) | not)]) as $unowned

  # Rules 1 to 3 for unowned applicable methods, per service.
  | ([$unowned | group_by(.service)[]
      | .[0].service as $s
      | ($by_id["service:" + $s]) as $si
      | if $si != null and ($si.status == "open" or ($si.status == "bot-closed" and ($s | IN($bound[]) | not))) then
          .[] | {key: .key, value: ("service:" + $s)}
        elif $si == null
             and (any($rows[]; .service == $s and .user_facing and .is_covered) | not)
             and (any($issues[]; .marker.kind == "method" and .marker.service == $s and .status != "human-closed") | not) then
          .[] | {key: .key, value: ("service:" + $s)}
        else
          .[] | {key: .key, value: ("method:" + .key)}
        end] | from_entries) as $joined

  # Groups: every open or bot-closed issue, plus new groups.
  | ([$applicable[] | select($owner[.key] != null or $joined[.key] != null)
      | {id: ($owner[.key] // $joined[.key]), key: .key}]) as $assigned
  | ([$issues[] | select(.status != "human-closed") | .marker.id]
     + [$assigned[].id] | unique) as $group_ids
  | [$group_ids[]
     | . as $id
     | $by_id[$id] as $issue
     | ([$assigned[] | select(.id == $id) | $by_key[.key]] | sort_by(.method)) as $grows
     | (if $issue != null then $issue.marker
        elif ($id | startswith("service:")) then {kind: "service", service: ($id | ltrimstr("service:"))}
        else ($id | ltrimstr("method:") | split(".")) as $p | {kind: "method", service: $p[0], method: $p[1]}
        end) as $m
     | ($m + {id: $id, first_seen: ($m.first_seen // $in.tag)}) as $group
     | {group: $group, issue: $issue, rows: $grows}
     | . + {ctx: {template: $template, tag: $in.tag, repo: $in.repo, core_repo: $in.core_repo,
                  group: $group, rows: $grows}}]
  | [.[]
     | . as $g
     | if $g.issue == null then
         if ($g.rows | length) > 0 then
           {action: "open", group: $g.group.id, title: ($g.group | title),
            body: render_body($g.ctx),
            labels: (base_labels + [$g.rows | readiness] | sort)}
         else empty end
       elif $g.issue.status == "open" and ($g.rows | length) > 0 then
         # FR-009, FR-023: rewrite a changed body, add or swap the base and
         # readiness labels; other labels stay.
         render_body($g.ctx) as $body
         | label_changes($g.issue.labels; $g.rows | readiness) as $lc
         | ($body != ($g.issue.body // "" | normalize)) as $changed
         | if $changed or ($lc.add | length) > 0 or ($lc.remove | length) > 0 then
             {action: "update", number: $g.issue.number, group: $g.group.id}
             + (if $changed then {body: $body} else {} end)
             + $lc.fields
           else empty end
       else empty end]
  | sort_by(.group, .action);

if ($ARGS.named.mode // "plan") == "needs-actors" then
  # Closed issues of every service with applicable methods (their closer
  # decides ownership, suppression, and grouping rules 1 and 2), and open
  # issues the plan would close (a human reopen blocks a close as covered).
  . as $in
  | rpc_rows as $rows
  | ([$rows[] | select(.applicable) | .service] | unique) as $app_services
  | (plan) as $actions
  | ([$in.issues[]? | . + {marker: (.body | parse_marker)} | select(.marker != null)
      | select(.state != "OPEN")
      | select(.marker.service | IN($app_services[]))
      | .number]
     + [if ($actions | type) == "array" then $actions[] | select(.action == "close") | .number else empty end])
  | unique
else
  plan as $actions
  | ([$actions[] | select(.action == "open")]) as $opens
  | if ($opens | length) > (.max_new // 10) then
      {error: "too-many-new", planned: [$opens[].title]}
    else $actions end
end
