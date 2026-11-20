# SPDX-License-Identifier: Apache-2.0
#
# rpc-marker.jq: the hidden marker of RPC coverage issues, shared by
# rpc-issues.jq and update-drift-issue.sh. Load it with
# `jq -L hack/automation 'include "rpc-marker"; ...'`.
#
# Marker on the first body line:
#   <!-- rpc-coverage: kind=service service=S first-seen=T methods=A,B -->
#   <!-- rpc-coverage: kind=method service=S method=M first-seen=T methods=M -->

def ident: test("^[A-Za-z0-9_]+$");

# Parse the marker on the first line of an issue body (a string or null).
# Returns {kind, service, method?, id, first_seen, methods}, or null when the
# first line is not a valid marker.
def parse_marker:
  ((. // "") | split("\n") | (.[0] // "") | sub("\r$"; "")) as $line
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
