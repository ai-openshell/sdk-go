# RPC coverage issue fixtures

`hack/automation/rpc-issues-test.sh` runs every case below; `mise run workflows:check` runs it in CI.

- `planner/<case>/input.json` is a planner input document (see the header of `hack/automation/rpc-issues.jq`), and `expected.json` is the action list (or error object) the planner must return for it.
  `rpc-issues-test.sh --update` rewrites every `expected.json` from the current planner; review the diff before committing.
  `backfill-v0.1.9/input.json` was produced by `rpc-issues.sh --tag v0.1.9 --dry-run --dump-input <file>`; the other cases are small hand-written documents.
  Where a case has `expected-actors.json`, the planner's `needs-actors` mode must return it; the `actors-unknown-*` cases hold the input as gathered, before `resolve_actors` fills in who closed or reopened an issue.
  `rpc-issues-test.sh` also runs `rpc-issues.sh --plan-input` on some cases to check the issue limit and the dry-run output.
- `coverage/` is a miniature SDK tree for the coverage helper `hack/automation/rpc-coverage.sh`: `rpc_covered_list coverage keys.txt` must print `expected.txt`.
  Its Go files sit under `testdata/`, so `go build` ignores them, but `gofmt` and the SPDX check still read them.
- `exceptions/` holds exceptions lists for `rpc_parse_exceptions` in `hack/automation/rpc-issues-lib.sh`: `valid.txt` must parse to its two entries, `missing-reason.txt` and `bad-key.txt` must fail with the line number.
- `drift-links/` checks `link_uncovered_rpcs` in `hack/automation/drift-issue.sh`: `report.md` with the coverage issues in `issues.json` must become `expected.md`.

Every `.json` file has a `.json.license` sidecar for the SPDX check.
