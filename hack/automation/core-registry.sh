# shellcheck shell=bash
# SPDX-License-Identifier: Apache-2.0

# core-registry.sh: sourced by start-gateway.sh and open-bump-pr.sh.
#
# core_registry <version> prints the registry that holds the core gateway
# image <version> (X.Y.Z or X.Y.Z-pre.N), or fails when neither has it yet.
# Releases since the move to Quay are on quay.io/ai-openshell; older ones stay
# on ghcr.io/nvidia/openshell for good.
core_registry() {
  local accept='Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json'
  local token
  if curl -fs -o /dev/null -H "$accept" "https://quay.io/v2/ai-openshell/gateway/manifests/$1"; then
    echo quay.io/ai-openshell
    return
  fi
  token=$(curl -fsS "https://ghcr.io/token?scope=repository:nvidia/openshell/gateway:pull" | jq -r .token) || return 1
  curl -fs -o /dev/null -H "Authorization: Bearer $token" -H "$accept" \
    "https://ghcr.io/v2/nvidia/openshell/gateway/manifests/$1" || return 1
  echo ghcr.io/nvidia/openshell
}
