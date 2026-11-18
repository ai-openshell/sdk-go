#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# rpc-issues.sh: open, update, close, and reopen one GitHub issue per core
# RPC (or per core service) that is user-facing and the Go SDK does not wrap.
#
# Usage: rpc-issues.sh [--dry-run] [--max-new <n>] [--tag <core-tag>]
#                      [--plan-input <file>] [--dump-input <file>]
#
# Run from the root of an sdk-go clone on main; coverage is read from the
# checkout. Gathers one input document (core's protos at the newest core tag
# or --tag as a buf image and raw files, core source references, SDK
# coverage, exceptions, existing issues), runs the planner rpc-issues.jq, and
# applies its actions with gh, or prints them with --dry-run. No GitHub write
# happens before the input is complete and the plan is computed.
#
#   --dry-run            print the planned actions, change nothing
#   --max-new <n>        limit for newly opened issues (default 10)
#   --tag <core-tag>     evaluate this core tag instead of the newest one
#   --plan-input <file>  skip gathering, plan this input document (implies
#                        --dry-run)
#   --dump-input <file>  write the assembled input document, then continue
#
# Environment: GH_TOKEN, BOT_LOGIN (the login of the bot that opens the
# issues, default "ai-openshell-sdk-go[bot]"; only issues it opened are
# managed), CORE_REPO (default NVIDIA/OpenShell), SDK_REPO (default
# ai-openshell/sdk-go), RPC_EXCEPTIONS (default
# hack/automation/rpc-exceptions.txt).
# Exit codes: 0 success, 1 failure, 2 usage error.
set -euo pipefail

core_repo=${CORE_REPO:-NVIDIA/OpenShell}
sdk_repo=${SDK_REPO:-ai-openshell/sdk-go}
bot_login=${BOT_LOGIN:-ai-openshell-sdk-go[bot]}
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/automation/rpc-issues-lib.sh
. "$here/rpc-issues-lib.sh"
# shellcheck source=hack/automation/rpc-coverage.sh
. "$here/rpc-coverage.sh"

usage() {
  [ $# -eq 0 ] || echo "ERROR: $*" >&2
  echo "usage: $0 [--dry-run] [--max-new <n>] [--tag <core-tag>] [--plan-input <file>] [--dump-input <file>]" >&2
  exit 2
}
die() { echo "ERROR: $*" >&2; exit 1; }

# retry <cmd...>: run an idempotent read up to three times, 10 and 20
# seconds apart. Never wrap a GitHub write: the next run converges instead.
retry() {
  local i name=$1
  [ "$name" != _to ] || name=$3
  for i in 1 2 3; do
    "$@" && return 0
    [ "$i" -eq 3 ] || { echo "WARNING: '$name' failed (attempt $i of 3), retrying" >&2; sleep $((i * 10)); }
  done
  return 1
}
# retry_to <file> <cmd...>: retry with stdout written to file, so a failed
# attempt leaves no partial output behind.
retry_to() {
  local out=$1
  shift
  retry _to "$out" "$@"
}
_to() { local out=$1; shift; "$@" >"$out"; }

dry=0
max_new=10
tag=
plan_input=
dump_input=
while [ $# -gt 0 ]; do
  case $1 in
  --dry-run) dry=1; shift ;;
  --max-new) [ $# -ge 2 ] || usage "--max-new needs a number"; max_new=$2; shift 2 ;;
  --tag) [ $# -ge 2 ] || usage "--tag needs a core tag"; tag=$2; shift 2 ;;
  --plan-input) [ $# -ge 2 ] || usage "--plan-input needs a file"; plan_input=$2; dry=1; shift 2 ;;
  --dump-input) [ $# -ge 2 ] || usage "--dump-input needs a file"; dump_input=$2; shift 2 ;;
  *) usage "unknown argument '$1'" ;;
  esac
done
[[ "$max_new" =~ ^[0-9]+$ ]] || usage "--max-new must be a non-negative integer, not '$max_new'"
[ -z "$tag" ] || [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-pre\.[0-9]+)?$ ]] ||
  usage "'$tag' is not a core release tag (vX.Y.Z or vX.Y.Z-pre.N)"
if [ -n "$plan_input" ]; then
  [ -f "$plan_input" ] || usage "$plan_input: no such file"
else
  [ "$(git rev-parse --show-toplevel)" = "$(pwd -P)" ] || die "run this from the root of the sdk-go clone"
fi
# --plan-input needs only jq.
tools="jq"
[ -n "$plan_input" ] || tools="jq gh git curl buf yq"
for tool in $tools; do
  command -v "$tool" >/dev/null || die "$tool not found (run 'mise install')"
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

plan() { # plan <input> [mode]
  jq -L "$here" -f "$here/rpc-issues.jq" --rawfile template "$here/rpc-issue-body.md" --arg mode "${2:-plan}" "$1"
}

# snake <PascalName>, camel <PascalName>
snake() { sed -E 's/([A-Z]+)([A-Z][a-z])/\1_\2/g; s/([a-z0-9])([A-Z])/\1_\2/g' <<<"$1" | tr '[:upper:]' '[:lower:]'; }
camel() { printf '%s%s\n' "$(tr '[:upper:]' '[:lower:]' <<<"${1:0:1}")" "${1:1}"; }

# refs <dir> <name> <fallback>: up to 3 files under core's <dir> that
# mention <name> as a word, else <fallback>.
refs() {
  local dir=$1 list
  [ -d "$work/core/$dir" ] || return 0
  list=$(cd "$work/core" && { grep -rlw -- "$2" "$dir" || grep -rlw -- "$3" "$dir" || true; } | LC_ALL=C sort)
  [ -z "$list" ] || head -3 <<<"$list"
}

# clone_core: a sparse checkout of core's sources at the tag in $work/core.
clone_core() {
  rm -rf "$work/core"
  git clone --quiet --depth=1 --filter=blob:none --no-checkout --branch "$tag" \
    "https://github.com/$core_repo.git" "$work/core" &&
    git -C "$work/core" sparse-checkout set crates/openshell-server/src crates/openshell-sdk/src sdk/typescript/src &&
    git -C "$work/core" checkout --quiet
}

gather() {
  local out=$1 f n raw_n
  if [ -z "$tag" ]; then
    retry_to "$work/tags.txt" "$here/core-tags.sh" --all --pin v0.0.0 || die "cannot list core tags"
    tag=$(tail -1 "$work/tags.txt")
    [ -n "$tag" ] || die "core $core_repo has no release tags"
  fi
  echo "Core tag: $tag" >&2

  # 1. Core's protos at the tag as a descriptor image.
  retry buf build "https://github.com/$core_repo.git#tag=$tag,depth=1" \
    --config '{"version":"v2","modules":[{"path":"proto"}]}' -o "$work/image.json" ||
    die "buf build of $core_repo at $tag failed"

  # 2. Raw proto files for signatures and messages; every rpc declaration
  # must be in the image.
  mkdir -p "$work/raw"
  echo '{}' >"$work/sources.json"
  for f in $(jq -r '.file[].name | select(startswith("google/") | not)' "$work/image.json"); do
    mkdir -p "$work/raw/$(dirname "$f")"
    curl -sSf --retry 3 --connect-timeout 20 --max-time 120 -o "$work/raw/$f" "https://raw.githubusercontent.com/$core_repo/$tag/proto/$f" ||
      die "cannot fetch proto/$f at $tag"
    n=$(jq --arg f "$f" '[.file[] | select(.name == $f) | .service[]?.method[]?] | length' "$work/image.json")
    # rpc declarations outside // comments, also several on one line.
    raw_n=$(sed 's#//.*##' "$work/raw/$f" | { grep -oE '(^|[;{}[:space:]])rpc[[:space:]]+[A-Za-z_]' || true; } | wc -l | tr -d ' ')
    [ "$n" = "$raw_n" ] || die "proto/$f at $tag declares $raw_n rpcs, but the buf image has $n"
    jq --arg f "$f" --rawfile text "$work/raw/$f" '.[$f] = $text' "$work/sources.json" >"$work/sources.tmp"
    mv "$work/sources.tmp" "$work/sources.json"
  done
  jq -e '[.file[].service[]?.method[]? | .options["[openshell.options.v1.authorization]"]
          | select(. != null) | select(.authMode == null)] | length == 0' "$work/image.json" >/dev/null ||
    die "an authorization annotation at $tag has no auth_mode"
  jq -e 'any(.file[] | select(.name == "openshell.proto") | .service[]?.method[]?;
             .options["[openshell.options.v1.authorization]"].authMode != null)' "$work/image.json" >/dev/null ||
    die "openshell.proto at $tag has no annotated methods; the authorization option was not parsed"

  # 3. Where core mentions each method: server, Rust SDK, TypeScript SDK.
  retry clone_core || die "cannot clone $core_repo at $tag"
  jq -r '.file[] | select(.name | startswith("google/") | not) | .service[]? | .name as $s | .method[] | "\($s).\(.name) \(.name)"' \
    "$work/image.json" | sort -u >"$work/methods.txt"
  local key method sn cm p
  while read -r key method; do
    sn=$(snake "$method"); cm=$(camel "$method")
    for p in $(refs crates/openshell-server/src "$sn" "$method"); do printf '%s\tserver_refs\t%s\n' "$key" "$p"; done
    for p in $(refs crates/openshell-sdk/src "$sn" "$method"); do printf '%s\trust_sdk_refs\t%s\n' "$key" "$p"; done
    for p in $(refs sdk/typescript/src "$cm" "$method"); do printf '%s\tts_sdk_refs\t%s\n' "$key" "$p"; done
  done <"$work/methods.txt" >"$work/refs.tsv"
  jq -Rn --arg base "https://github.com/$core_repo/blob/$tag/" '
    reduce (inputs | select(length > 0) | split("\t")) as $r ({};
      .[$r[0]][$r[1]] += [{path: $r[2], url: ($base + $r[2])}])' "$work/refs.tsv" >"$work/refs.json"

  # 4. One row per method of every non-google file.
  jq -n --slurpfile img "$work/image.json" --slurpfile src "$work/sources.json" \
    --slurpfile refs "$work/refs.json" --arg core_repo "$core_repo" --arg tag "$tag" '
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
     | .value.method | to_entries[] | .key as $mi | .value as $m
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
       }]' >"$work/rpcs.json"
  jq -e 'length > 0 and all(.[]; .signature != "" and .proto_url != "")' "$work/rpcs.json" >/dev/null ||
    die "cannot locate every rpc declaration of $tag in its proto source"

  # 5. SDK coverage, generated bindings, bindings generation, bump branch.
  jq -r '.[] | "\(.service).\(.method)"' "$work/rpcs.json" | sort -u >"$work/keys.txt"
  rpc_covered_list . "$work/keys.txt" >"$work/covered.txt" || die "cannot determine the RPCs the SDK covers"
  rpc_bound_services . >"$work/bound.txt" || die "cannot determine the services the SDK binds"
  grep -ho '_FullMethodName *= *"/[^"]*"' proto/*/*_grpc.pb.go 2>/dev/null |
    sed -E 's#.*"/([A-Za-z0-9_]+\.)*([A-Za-z0-9_]+)/([A-Za-z0-9_]+)"#\2.\3#' | sort -u >"$work/built.txt" || true
  [ -s "$work/built.txt" ] || die "no FullMethodName constants in proto/*/*_grpc.pb.go"
  yq -r '.plugins[0].opt[] | select(test("^M")) | sub("^M"; "") | sub("=.*"; "")' buf.gen.yaml >"$work/generated.txt" ||
    die "cannot read the M options of buf.gen.yaml"
  local bump=false
  retry_to "$work/branches.txt" gh api "repos/$sdk_repo/git/matching-refs/heads/core/$tag" --jq '.[].ref' ||
    die "cannot list branches of $sdk_repo"
  if grep -qxF "refs/heads/core/$tag" "$work/branches.txt"; then bump=true; fi

  # 6. Exceptions and existing issues.
  rpc_parse_exceptions "${RPC_EXCEPTIONS:-hack/automation/rpc-exceptions.txt}" >"$work/exceptions.json" || exit 1
  # The REST issue list, not gh issue list --label (the search index lags
  # behind writes). Only issues the bot opened count, so a marker in
  # someone else's issue cannot steer the automation.
  retry_to "$work/issues.ndjson" gh api --paginate "repos/$sdk_repo/issues?labels=rpc-coverage&state=all&per_page=100" \
    --jq '.[] | select(.pull_request == null)
          | {number, state: (.state | ascii_upcase), title, body: (.body // ""),
             labels: [.labels[].name], author: .user.login}' ||
    die "cannot list the rpc-coverage issues of $sdk_repo"
  jq -s --arg bot "$bot_login" 'map(select(.author == $bot) | del(.author))' "$work/issues.ndjson" >"$work/issues.json"

  jq -n --arg repo "$sdk_repo" --arg core_repo "$core_repo" --arg tag "$tag" \
    --argjson max_new "$max_new" --argjson bump "$bump" \
    --slurpfile rpcs "$work/rpcs.json" --slurpfile exceptions "$work/exceptions.json" \
    --slurpfile issues "$work/issues.json" \
    --rawfile covered "$work/covered.txt" --rawfile bound "$work/bound.txt" \
    --rawfile built "$work/built.txt" --rawfile generated "$work/generated.txt" '
    def lines: split("\n") | map(select(length > 0));
    {repo: $repo, core_repo: $core_repo, tag: $tag, max_new: $max_new,
     rpcs: $rpcs[0], services_bound: ($bound | lines), covered: ($covered | lines),
     built: ($built | lines), generated_files: ($generated | lines),
     bump_branch_exists: $bump, exceptions: $exceptions[0],
     issues: [$issues[0][] | . + {last_actor_is_bot: null, last_event: null}]}' >"$out"
}

# Fill last_event and last_actor_is_bot for the issues the plan depends on.
resolve_actors() {
  local in=$1 fetched=" " need n last event login
  while :; do
    need=$(plan "$in" needs-actors | jq -r '.[]') || die "planning failed"
    local new=
    for n in $need; do
      case $fetched in *" $n "*) ;; *) new+="$n " ;; esac
    done
    [ -n "$new" ] || break
    for n in $new; do
      retry_to "$work/events.txt" gh api --paginate "repos/$sdk_repo/issues/$n/events" \
        --jq '.[] | select(.event == "closed" or .event == "reopened") | "\(.event) \(.actor.login // "")"' ||
        die "cannot read the events of #$n"
      last=$(tail -1 "$work/events.txt")
      event=${last%% *}; login=${last#* }
      [ -z "$event" ] || echo "#$n: last $event by $login" >&2
      jq --argjson n "$n" --arg event "$event" --arg login "$login" --arg bot "$bot_login" '
        .issues |= map(if .number == $n then
          .last_event = (if $event == "" then null else $event end)
          | .last_actor_is_bot = (if $event == "" then null else $login == $bot end)
        else . end)' "$in" >"$in.tmp"
      mv "$in.tmp" "$in"
      fetched+="$n "
    done
  done
}

if [ -n "$plan_input" ]; then
  cp "$plan_input" "$work/input.json"
else
  gather "$work/input.json"
  resolve_actors "$work/input.json"
fi
jq --argjson max_new "$max_new" '.max_new = $max_new' "$work/input.json" >"$work/input.tmp" && mv "$work/input.tmp" "$work/input.json"
[ -z "$dump_input" ] || cp "$work/input.json" "$dump_input"

plan "$work/input.json" >"$work/plan.json" || die "planning failed"
if jq -e 'type == "object" and .error == "too-many-new"' "$work/plan.json" >/dev/null; then
  echo "ERROR: the plan opens $(jq '.planned | length' "$work/plan.json") new issues, more than --max-new $max_new; nothing was changed:" >&2
  jq -r '.planned[] | "  - \(.)"' "$work/plan.json" >&2
  echo "Raise the limit with --max-new (core-watch input max_new_issues) if these are expected." >&2
  exit 1
fi

tag=$(jq -r .tag "$work/input.json")
count() { jq --arg a "$1" '[.[] | select(.action == $a)] | length' "$work/plan.json"; }
summary="core $tag: $(count open) to open, $(count update) to update, $(count close) to close, $(count reopen) to reopen"

if [ "$dry" -eq 1 ]; then
  echo "dry-run: $summary"
  jq -r '.[] |
    def indent: split("\n") | map("  | " + .) | join("\n");
    def labels: ((.add_labels // []) | map("+" + .)) + ((.remove_labels // []) | map("-" + .))
      | if length > 0 then " (labels " + join(" ") + ")" else "" end;
    if .action == "open" then
      "\ndry-run: open \"\(.title)\" (labels \(.labels | join(", ")))\n\(.body | indent)"
    elif .action == "update" then
      "\ndry-run: update #\(.number) \(.group)\(labels)" + (if .body then "\n" + (.body | indent) else "" end)
    elif .action == "close" then
      "\ndry-run: close #\(.number) \(.group) with comment:\n\(.comment | indent)"
    else
      "\ndry-run: reopen #\(.number) \(.group)\(labels) with comment:\n\(.comment | indent)\n  body:\n\(.body | indent)"
    end' "$work/plan.json"
  exit 0
fi

n=$(jq length "$work/plan.json")
if [ "$n" -eq 0 ]; then
  echo "Nothing to do for $summary."
  exit 0
fi
echo "Applying: $summary"

# Create the labels the plan applies and the repository lacks.
label_desc() {
  case $1 in
  rpc-coverage) echo "Core RPC the Go SDK does not wrap yet 1d76db" ;;
  rpc:ready) echo "Coverage issue buildable on main now 0e8a16" ;;
  rpc:blocked) echo "Coverage issue waiting for a core bump or bindings generation d93f0b" ;;
  "help wanted") echo "Extra attention is needed 008672" ;;
  esac
}
needed=$(jq -r '[.[] | (.labels // []) + (.add_labels // []) | .[]] | unique | .[]' "$work/plan.json")
if [ -n "$needed" ]; then
  retry_to "$work/labels.txt" gh label list -R "$sdk_repo" --limit 500 --json name --jq '.[].name' || die "cannot list labels"
  existing=$(cat "$work/labels.txt")
  while IFS= read -r l; do
    grep -qxF "$l" <<<"$existing" && continue
    d=$(label_desc "$l")
    [ -n "$d" ] || die "no description for label $l"
    gh label create "$l" -R "$sdk_repo" --description "${d% *}" --color "${d##* }" >/dev/null
    echo "created label $l"
  done <<<"$needed"
fi

for i in $(seq 0 $((n - 1))); do
  jq ".[$i]" "$work/plan.json" >"$work/action.json"
  action=$(jq -r .action "$work/action.json")
  num=$(jq -r '.number // empty' "$work/action.json")
  jq -r '.body // empty' "$work/action.json" >"$work/body.md"
  label_args=()
  while IFS= read -r l; do [ -z "$l" ] || label_args+=(--add-label "$l"); done < <(jq -r '(.add_labels // [])[]' "$work/action.json")
  while IFS= read -r l; do [ -z "$l" ] || label_args+=(--remove-label "$l"); done < <(jq -r '(.remove_labels // [])[]' "$work/action.json")
  case $action in
  open)
    title=$(jq -r .title "$work/action.json")
    open_args=()
    while IFS= read -r l; do open_args+=(--label "$l"); done < <(jq -r '.labels[]' "$work/action.json")
    url=$(gh issue create -R "$sdk_repo" --title "$title" --body-file "$work/body.md" "${open_args[@]}")
    echo "opened #${url##*/} $title"
    ;;
  update)
    edit_args=("${label_args[@]}")
    [ ! -s "$work/body.md" ] || edit_args+=(--body-file "$work/body.md")
    gh issue edit "$num" -R "$sdk_repo" "${edit_args[@]}" >/dev/null
    echo "updated #$num $(jq -r .group "$work/action.json")"
    ;;
  close)
    gh issue close "$num" -R "$sdk_repo" --comment "$(jq -r .comment "$work/action.json")" >/dev/null
    echo "closed #$num $(jq -r .group "$work/action.json")"
    ;;
  reopen)
    gh issue reopen "$num" -R "$sdk_repo" --comment "$(jq -r .comment "$work/action.json")" >/dev/null
    gh issue edit "$num" -R "$sdk_repo" --body-file "$work/body.md" "${label_args[@]}" >/dev/null
    echo "reopened #$num $(jq -r .group "$work/action.json")"
    ;;
  *) die "unknown action $action" ;;
  esac
done
