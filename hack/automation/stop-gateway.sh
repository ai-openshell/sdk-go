#!/usr/bin/env bash
# stop-gateway.sh: stop the E2E gateway started by start-gateway.sh, save its
# log, and remove the sandbox containers it created.
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
echo "Gateway stopped."
