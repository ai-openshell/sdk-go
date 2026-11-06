# Validate Client Configuration

Prerequisites: repository toolchain through mise; no running gateway required.

1. Run `mise exec -- go test ./openshell/v1/internal/grpc ./openshell/v1 ./openshell/v1/gateway -run 'ClientPolicy|ClientOptions' -count=1 -v`.
2. Check default/explicit deadlines, bounded safe-read retries, unsafe exclusions, caller cancellation, and preserved stream/polling lifetime behavior.
3. Check logger metadata and synthetic-secret exclusion in captured output.
4. Run `mise run test`, `mise run format:check ::: lint ::: build ::: docs:check`, and `mise run docs:build`.
5. Check `docs/src/api/client.md` and `docs/src/api/gateway.md` against observed behavior and run `mise run ci` for full branch checks.

Expected: all behavior checks and existing tests pass without changes to generated bindings, dependencies, public configuration signatures, or gateway infrastructure.

## Implementation evidence (2026-11-06)

- T001–T005 each completed with a passing full `mise exec -- go test ./...` run.
- Timeout, retry recovery, missing logger diagnostics, and gateway negative-timeout
  regressions were observed before their corresponding behavior changes.
- Correctness checkpoints after T002 and T004 each reported zero findings.
- Final `mise run ci` exited 0: formatting, lint (zero issues), build, race-enabled
  tests with coverage, pinned protobuf, conformance and documentation checks passed.
  Log: `/private/tmp/go-sdk-client-config-ci.log`.
- `mise run docs:build` exited 0. Log:
  `/private/tmp/go-sdk-client-config-docs-build.log`.
- Conformance checks retain the existing warning that core v0.1.7 does not yet
  publish the OIDC token-bundle fixture; the existing skip passed as configured.
- Generated bindings, dependency files and public configuration signatures are
  unchanged. No live gateway or sandbox infrastructure was needed.
