# SPDX-License-Identifier: Apache-2.0
#
# rpc-rows.jq: one input row per method of every non-google proto file, from
# core's buf image at a tag. Used by rpc-issues.sh (gather, step 4); tested by
# rpc-issues-test.sh on testdata/rpc-issues/rows/.
#
#   jq -n -f rpc-rows.jq --slurpfile img image.json --slurpfile src sources.json \
#     --slurpfile refs refs.json --arg core_repo NVIDIA/OpenShell --arg tag vX.Y.Z
#
# image.json: the buf image (`buf build -o image.json`) with source info;
# sources.json: {"<file>": "<proto source text>"}; refs.json:
# {"<Service.Method>": {server_refs, rust_sdk_refs, ts_sdk_refs}}.
# Signatures and messages are cut from the source text by the spans of the
# image's sourceCodeInfo; spans have 4 elements, or 3 for a single line.

def dedent: (map(select(test("\\S")) | match("^ *").length) | min // 0) as $n
  | map(if length >= $n then .[$n:] else . end);
def comment: (. // "") | split("\n") | map(sub("^ "; "") | sub("\\s+$"; "")) | join("\n") | sub("^\\s+"; "") | sub("\\s+$"; "");
def last_line: if length == 4 then .[2] else .[0] end;
$img[0] as $img | $src[0] as $src | $refs[0] as $refs
| ([$img.file[] | .package as $p | .name as $f | (.messageType // []) | to_entries[]
    | {key: ("." + (if $p then $p + "." else "" end) + .value.name), value: {file: $f, index: .key}}]
   | from_entries) as $msgs
| ([$img.file[] | {key: .name, value: ([.sourceCodeInfo.location[]?
      | select((.path | length) == 2 and .path[0] == 4 or ((.path | length) == 4 and .path[0] == 6 and .path[2] == 2))
      | {key: (.path | map(tostring) | join(".")), value: .}] | from_entries)}] | from_entries) as $locs
| def cut($f; $span): ($src[$f] // "" | split("\n"))[$span[0]:($span | last_line) + 1] | dedent | join("\n");
def message($t): ($t | ltrimstr(".")) as $name
  | $msgs[$t] as $m
  | if $m == null then {name: $name, source: ""}
    else {name: $name, source: (($locs[$m.file][("4." + ($m.index | tostring))].span) as $sp
                                | if $sp == null then "" else cut($m.file; $sp) end)} end;
[$img.file[] | select(.name | startswith("google/") | not) | .name as $f
 | (.service // []) | to_entries[] | .key as $si | .value.name as $s
 | (.value.method // []) | to_entries[] | .key as $mi | .value as $m
 | $locs[$f]["6.\($si).2.\($mi)"] as $loc
 | ($m.options["[openshell.options.v1.authorization]"] // {}) as $auth
 | {
     file: $f, service: $s, method: $m.name,
     auth_mode: $auth.authMode, scope: $auth.scope,
     workspace_role: $auth.workspaceRole, global_role: $auth.globalRole,
     client_streaming: ($m.clientStreaming // false), server_streaming: ($m.serverStreaming // false),
     request: ($m.inputType | ltrimstr(".")), response: ($m.outputType | ltrimstr(".")),
     comment: ($loc.leadingComments | comment),
     signature: (if $loc == null then "" else cut($f; $loc.span) end),
     messages: ([$m.inputType, $m.outputType] | unique | map(message(.))),
     proto_url: (if $loc == null then ""
                 else "https://github.com/\($core_repo)/blob/\($tag)/proto/\($f)#L\($loc.span[0] + 1)-L\(($loc.span | last_line) + 1)" end),
     server_refs: ($refs["\($s).\($m.name)"].server_refs // []),
     rust_sdk_refs: ($refs["\($s).\($m.name)"].rust_sdk_refs // []),
     ts_sdk_refs: ($refs["\($s).\($m.name)"].ts_sdk_refs // [])
   }]
