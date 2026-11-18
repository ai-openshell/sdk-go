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

# Body of a group; $ctx carries the input document and the group's rows.
def render_body($ctx):
  $ctx.group as $g
  | ($g + {methods: [$ctx.rows[].method]}) as $m
  | $ctx.template
  | fill({
      MARKER: ($m | render_marker),
      SUMMARY: (
        if $g.kind == "service" then
          "Core `\($ctx.tag)` has \($ctx.rows | length) user-facing method(s) in the `\($g.service)` service that the Go SDK does not wrap yet."
        else
          "Core `\($ctx.tag)` has `\($g.service).\($g.method)`, a user-facing RPC the Go SDK does not wrap yet."
        end)
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
