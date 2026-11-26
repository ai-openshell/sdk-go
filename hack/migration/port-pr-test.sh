#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

# port-pr-test.sh: offline test of port-pr.sh against a synthetic upstream PR.
#
# Usage: port-pr-test.sh
#
# Run from the root of an sdk-go clone; part of workflows:check. Builds a
# throwaway SDK clone and a throwaway core repository whose PR replaces
# hack/automation/fill-template.sh with a script that writes a marker file
# and puts {{GH_TOKEN}} into a commit subject. gh and go are stubbed, and core
# is fetched from the local repository, so nothing touches the network.
# Checks that no code from the PR runs during the port, that the PR body keeps
# the upstream text literally, that --continue from the patched working tree
# is refused, and that --continue from the trusted copy finishes the port.
# Exits 1 on any failure.
set -euo pipefail

repo=$(pwd -P)
tmp=$(mktemp -d)
trap '[ -n "${KEEP:-}" ] || rm -rf "$tmp"' EXIT # KEEP=1 keeps it for debugging
sdk=$tmp/sdk core=$tmp/core bin=$tmp/bin marker=$tmp/pwned

fail=0
ok() { echo "ok $1"; }
bad() { echo "FAIL $1: $2"; fail=1; }

gitq() { git -c user.name=Test -c user.email=test@example.com -c init.defaultBranch=main "$@"; }

# The SDK clone: the migration and template files of this checkout.
toolkit=(hack/migration/port-pr.sh hack/migration/port-pr-body.md hack/migration/FREEZE
  hack/automation/fill-template.sh hack/automation/template.jq)
mkdir -p "$sdk"
for f in "${toolkit[@]}"; do
  mkdir -p "$sdk/$(dirname "$f")"
  cp "$repo/$f" "$sdk/$f"
done
printf 'module github.com/ai-openshell/sdk-go\n\ngo 1.22\n' >"$sdk/go.mod"
printf 'package sdk\n' >"$sdk/sdk.go"
gitq -C "$sdk" init -q
gitq -C "$sdk" add -A
gitq -C "$sdk" commit -q -m "sdk base"

# Core with sdk/go holding the same renderer, and a PR that replaces it.
mkdir -p "$core/sdk/go/hack/automation"
printf 'module github.com/NVIDIA/OpenShell/sdk/go\n\ngo 1.22\n' >"$core/sdk/go/go.mod"
printf 'package sdk\n' >"$core/sdk/go/sdk.go"
cp "$repo/hack/automation/fill-template.sh" "$core/sdk/go/hack/automation/fill-template.sh"
gitq -C "$core" init -q
gitq -C "$core" add -A
gitq -C "$core" commit -q -m "core base"
printf '#!/usr/bin/env bash\ntouch %q\n' "$marker" >"$core/sdk/go/hack/automation/fill-template.sh"
printf 'package sdk\n\n// Added by the PR.\nfunc Added() {}\n' >"$core/sdk/go/added.go"
gitq -C "$core" add -A
gitq -C "$core" commit -q -m 'feat: add Added {{GH_TOKEN}} {{AUTHOR}}'
head=$(git -C "$core" rev-parse HEAD)
git -C "$core" update-ref refs/pull/1/head "$head"
git -C "$core" reset -q --hard HEAD~1

# gh answers the PR metadata; go is never needed with --no-build.
mkdir -p "$bin"
cat >"$bin/gh" <<EOF
#!/usr/bin/env bash
printf '%s\n' '{"state":"OPEN","baseRefName":"main","headRefOid":"$head","title":"feat: add Added","url":"https://example.com/pr/1","author":{"login":"someone"}}'
EOF
printf '#!/usr/bin/env bash\nexit 0\n' >"$bin/go"
chmod +x "$bin/gh" "$bin/go"

port() { # runs in the SDK clone with the stubs first on PATH
  (cd "$sdk" && PATH="$bin:$PATH" GH_TOKEN=ghs_SECRET \
    GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.com \
    GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.com "$@")
}

# 1. A full port: the PR's replacement renderer must not run.
if port hack/migration/port-pr.sh --base main --core "$core" --no-build 1 >"$tmp/run.out" 2>&1; then
  ok port-runs
else
  bad port-runs "exit $?: $(tail -n 3 "$tmp/run.out")"
fi
if [ -e "$marker" ]; then bad trusted-renderer "the PR's fill-template.sh ran"; else ok trusted-renderer; fi
if grep -q 'Added() {}' "$sdk/added.go" 2>/dev/null; then ok patches-applied; else bad patches-applied "added.go missing"; fi
body=$(cd "$sdk" && cat "$(git rev-parse --git-path port-src/1)/body.md" 2>/dev/null || true)
case $body in
  *'{{GH_TOKEN}} {{AUTHOR}}'*) ok body-literal ;;
  *) bad body-literal "upstream subject not literal in the body" ;;
esac
case $body in *ghs_SECRET*) bad body-no-secret "token in the body" ;; *) ok body-no-secret ;; esac

# 2. --continue from the patched working tree is refused.
if port hack/migration/port-pr.sh --base main --continue 1 >"$tmp/cont.out" 2>&1; then
  bad continue-refused "the working-tree script ran --continue"
elif grep -q 'not trusted' "$tmp/cont.out"; then
  ok continue-refused
else
  bad continue-refused "$(tail -n 2 "$tmp/cont.out")"
fi

if port hack/migration/port-pr.sh --fix-paths 1 >"$tmp/fix.out" 2>&1; then
  bad fix-paths-refused "the working-tree script ran --fix-paths"
elif grep -q 'not trusted' "$tmp/fix.out"; then
  ok fix-paths-refused
else
  bad fix-paths-refused "$(tail -n 2 "$tmp/fix.out")"
fi

# 3. --continue from the trusted copy finishes the port.
trusted=$(cd "$sdk" && cd "$(git rev-parse --git-path port-src/1)/trusted" 2>/dev/null && pwd -P || true)
if [ -n "$trusted" ] && port "$trusted/hack/migration/port-pr.sh" --base main --continue --no-build 1 >"$tmp/trusted.out" 2>&1; then
  ok continue-trusted
else
  bad continue-trusted "$(tail -n 3 "$tmp/trusted.out" 2>/dev/null)"
fi
if [ -e "$marker" ]; then bad trusted-renderer-continue "the PR's fill-template.sh ran"; else ok trusted-renderer-continue; fi

[ "$fail" -eq 0 ] && echo "port-pr tests passed"
exit "$fail"
