#!/usr/bin/env bash
# stop-gateway.sh: stop the E2E gateway started by start-gateway.sh, save its
# log, remove the sandbox containers, and remove the "e2e" gateway
# registration (and the active-gateway pointer when it still names "e2e").
#
# Usage: stop-gateway.sh [--state-dir <dir>]
#
# Writes <state-dir>/gateway.log. Safe to run when nothing is running.
set -uo pipefail

state=${OPENSHELL_E2E_STATE:-${RUNNER_TEMP:-/tmp}/openshell-e2e}
[ "${1:-}" != --state-dir ] || state=${2:?--state-dir needs a path}
[ -f "$state/docker-compose.yml" ] || { echo "No E2E gateway state in $state."; exit 0; }

semver=$(sed 's/^v//' "$state/core-version" 2>/dev/null || echo latest)
compose() { IMAGE_TAG=$semver docker compose -p openshell-e2e -f "$state/docker-compose.yml" "$@"; }

compose logs --no-color --timestamps >"$state/gateway.log" 2>&1
echo "Gateway log: $state/gateway.log"
# Sandbox containers carry the namespace label from core's gateway.toml.
ids=$(docker ps -aq --filter label=openshell.sandbox_namespace=openshell)
[ -z "$ids" ] || docker rm -f $ids >/dev/null
compose down --volumes --remove-orphans >/dev/null 2>&1

config_home=${XDG_CONFIG_HOME:-$HOME/.config}
rm -rf "$config_home/openshell/gateways/e2e"
if [ "$(cat "$config_home/openshell/active_gateway" 2>/dev/null)" = e2e ]; then
  rm -f "$config_home/openshell/active_gateway"
fi
echo "Gateway stopped."
