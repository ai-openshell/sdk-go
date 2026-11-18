# RPC coverage issue fixtures

`hack/automation/rpc-issues-test.sh` runs every case below; `mise run workflows:check` runs it in CI.

- `planner/<case>/input.json` is a planner input document (see the header of `hack/automation/rpc-issues.jq`), and `expected.json` is the action list (or error object) the planner must return for it.
  `rpc-issues-test.sh --update` rewrites every `expected.json` from the current planner; review the diff before committing.
  `backfill-v0.1.9/input.json` was produced by `rpc-issues.sh --tag v0.1.9 --dry-run --dump-input <file>`; the other cases are small hand-written documents.
- `coverage/` is a miniature SDK tree for the coverage helper `hack/automation/rpc-coverage.sh`: `rpc_covered_list coverage keys.txt` must print `expected.txt`.
  Its Go files sit under `testdata/`, so `go build` ignores them, but `gofmt` and the SPDX check still read them.
- `exceptions/` holds exceptions lists for `rpc_parse_exceptions` in `hack/automation/rpc-issues-lib.sh`: `valid.txt` must parse, `missing-reason.txt` must fail with the line number.

Every `.json` file has a `.json.license` sidecar for the SPDX check.
