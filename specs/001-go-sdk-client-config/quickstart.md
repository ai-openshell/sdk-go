# Validate Client Configuration

Prerequisites: repository toolchain through mise; no running gateway required.

1. Run `mise exec -- go test ./openshell/v1/internal/grpc ./openshell/v1 ./openshell/v1/gateway -run 'ClientPolicy|ClientOptions' -count=1 -v`.
2. Check default/explicit deadlines, bounded safe-read retries, unsafe exclusions, caller cancellation, and preserved stream/polling lifetime behavior.
3. Check logger metadata and synthetic-secret exclusion in captured output.
4. Run `mise run test`, `mise run format:check ::: lint ::: build ::: docs:check`, and `mise run docs:build`.
5. Check `docs/src/api/client.md` and `docs/src/api/gateway.md` against observed behavior and run `mise run ci` for full branch checks.

Expected: all behavior checks and existing tests pass without changes to generated bindings, dependencies, public configuration signatures, or gateway infrastructure.
