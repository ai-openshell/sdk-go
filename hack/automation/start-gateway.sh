#!/usr/bin/env bash
# start-gateway.sh: run the published OpenShell gateway for a core version
# with the Docker compute driver, for the E2E lane.
#
# Usage: start-gateway.sh <core-version> [--state-dir <dir>]
#
# Uses core's own deployment files, deploy/docker/docker-compose.yml and
# gateway.toml, at the core tag: an init container generates the supervisor
# JWT keys, and the gateway serves plaintext gRPC on 127.0.0.1:8080 and
# /readyz on 127.0.0.1:8081. The supervisor and sandbox images are pinned to
# the same version instead of :latest. Then registers the gateway as "e2e"
# for the SDK in $XDG_CONFIG_HOME/openshell. Waits up to 15 minutes for the
# gateway image of a fresh tag to be published.
#
# Needs docker with the compose plugin and a host path /var/lib/openshell
# (core's compose file bind-mounts it at the same path). stop-gateway.sh
# undoes everything.
set -euo pipefail

core_repo=${CORE_REPO:-NVIDIA/OpenShell}
registry=ghcr.io/nvidia/openshell
name=e2e
port=8080
health_port=8081

tag=
state=${OPENSHELL_E2E_STATE:-${RUNNER_TEMP:-/tmp}/openshell-e2e}
while [ $# -gt 0 ]; do
  case $1 in
  --state-dir) state=${2:?--state-dir needs a path}; shift 2 ;;
  -*) echo "usage: $0 <core-version> [--state-dir <dir>]" >&2; exit 2 ;;
  *) tag=$1; shift ;;
  esac
done
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-pre\.[0-9]+)?$ ]] || {
  echo "usage: $0 <core-version>   (vX.Y.Z or vX.Y.Z-pre.N)" >&2
  exit 2
}
semver=${tag#v}
mkdir -p "$state"
echo "$tag" >"$state/core-version"

# Core pushes images after tagging; a proposal can be faster than that.
manifest_exists() {
  local token
  token=$(curl -fsS "https://ghcr.io/token?scope=repository:nvidia/openshell/gateway:pull" | jq -r .token) || return 1
  curl -fsS -o /dev/null -H "Authorization: Bearer $token" \
    -H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json' \
    "https://ghcr.io/v2/nvidia/openshell/gateway/manifests/$semver"
}
for i in $(seq 1 15); do
  manifest_exists && break
  if [ "$i" -eq 15 ]; then
    echo "ERROR: gateway image for $semver not published yet, re-run later" >&2
    exit 1
  fi
  echo "Waiting for $registry/gateway:$semver ($i/15) ..."
  sleep 60
done

# Core's deployment files at the tag.
base="https://raw.githubusercontent.com/$core_repo/$tag/deploy/docker"
curl -fsSL "$base/docker-compose.yml" -o "$state/docker-compose.yml"
curl -fsSL "$base/gateway.toml" -o "$state/gateway.toml"
sed -i.bak \
  -e "s#\"$registry/supervisor:latest\"#\"$registry/supervisor:$semver\"#" \
  -e "s#\"$registry/sandbox:latest\"#\"$registry/sandbox:$semver\"#" \
  "$state/gateway.toml" && rm -f "$state/gateway.toml.bak"
grep -q "supervisor:$semver" "$state/gateway.toml" || {
  echo "ERROR: could not pin the supervisor image in core's gateway.toml at $tag" >&2
  exit 1
}

compose() { IMAGE_TAG=$semver docker compose -p openshell-e2e -f "$state/docker-compose.yml" "$@"; }
echo "Starting gateway $semver ..."
compose up -d --quiet-pull

for i in $(seq 1 90); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$health_port/readyz"; then
    break
  fi
  if [ "$i" -eq 90 ]; then
    echo "ERROR: gateway not ready after 180 s" >&2
    compose logs --no-color gateway | tail -n 80 >&2 || true
    exit 1
  fi
  sleep 2
done
echo "Gateway $semver ready on 127.0.0.1:$port."

config_home=${XDG_CONFIG_HOME:-$HOME/.config}
dir="$config_home/openshell/gateways/$name"
mkdir -p "$dir"
cat >"$dir/metadata.json" <<JSON
{
  "name": "$name",
  "gateway_endpoint": "http://127.0.0.1:$port",
  "is_remote": false,
  "gateway_port": $port,
  "auth_mode": "plaintext"
}
JSON
printf '%s' "$name" >"$config_home/openshell/active_gateway"
echo "Registered gateway '$name' in $config_home/openshell; run the suite with OPENSHELL_GATEWAY=$name."
