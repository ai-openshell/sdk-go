# Automation scripts

Every workflow under `.github/workflows/` wraps one of these scripts through a mise task, so each job can be run locally before it runs in CI.
Scripts that write to GitHub take `--dry-run` and print what they would do instead.
Workflows run them with a token of the `ai-openshell-sdk-go` GitHub App (`ai-openshell-sdk-go[bot]`; secrets `SDK_BOT_CLIENT_ID` and `SDK_BOT_PRIVATE_KEY` in the `sdk-bot` environment, which only `main` can deploy to), never with `GITHUB_TOKEN`, so their pushes and PRs trigger CI.

| File | mise task | Purpose |
|------|-----------|---------|
| `core-tags.sh` | `core:tags` | List ai-openshell/openshell release tags newer than the pin (`buf.gen.yaml`, `inputs[0].tag`), semver-sorted with pre-releases below their stable version. |
| `open-bump-pr.sh <tag> [--dry-run]` | `core:bump` | Open the update proposal for one core tag: branch `core/<tag>`, pin change, regenerated bindings, proto diff in the body, labels `core-bump` plus `stable` or `prerelease`. Closes older pre-release proposals. |
| `bump-pr-body.md` | | PR body template for `open-bump-pr.sh`. |
| `core-registry.sh` | | Sourced by `start-gateway.sh` and `open-bump-pr.sh`: `core_registry <version>` prints the registry that holds a core release's images, `quay.io/ai-openshell` for releases since the move and `ghcr.io/nvidia/openshell` for older ones. |
| `merge-bump-pr.sh [--dry-run] [--auto-merge on\|off]` | `core:merge` | Merge stable proposals that change no proto: one bot commit touching only the pin and the README (nothing under `proto/`), green checks, no `hold` label. Off unless `CORE_AUTO_MERGE` is `on`. |
| `tag-release.sh [--dry-run]` | `release:tag` | After a stable pin lands on `main`: create the lockstep tag `vX.Y.Z` and the GitHub release, then ask the Go module proxy for the version. |
| `drift-check.sh --out <file>` | `core:drift` | Compare core `main` against the pin: `buf breaking`, regenerate and build, uncovered RPCs. Writes the markdown report. |
| `update-drift-issue.sh <report.md>` | | Create, update, or close the single `Core main drift` issue from a report; uncovered RPCs link to their open RPC coverage issues. |
| `rpc-issues.sh [--dry-run] [--max-new <n>] [--tag <tag>]` | `core:rpc-issues` | Open, update, close, and reopen one issue per user-facing core RPC (or service) the SDK does not wrap, for the newest core tag. `BOT_LOGIN` names the bot (default `ai-openshell-sdk-go[bot]`); `--plan-input <file>` plans a saved input document, `--dump-input <file>` saves one. |
| `rpc-issues.jq` | | The planner for `rpc-issues.sh`: input document in, actions and rendered bodies out; no network. |
| `rpc-rows.jq` | | Turns core's buf image and proto sources into the planner's input rows (signatures, comments, messages, links). |
| `rpc-marker.jq` | | The hidden issue marker, parsed by `rpc-issues.jq` and `update-drift-issue.sh` (jq module). |
| `rpc-issue-body.md` | | Issue body template for `rpc-issues.jq`. |
| `rpc-coverage.sh` | | The coverage rule shared by `rpc-issues.sh` and `drift-check.sh` (sourced, not run). |
| `rpc-issues-lib.sh` | | Exceptions list parser and helpers for listing the bot's issues (sourced, not run). |
| `rpc-exceptions.txt` | | RPCs the SDK deliberately does not wrap, one `Service.Method  reason` per line. |
| `rpc-issues-test.sh [--update]` | | Offline planner, `rpc-issues.sh --plan-input`, coverage, exceptions, and drift link tests on `testdata/rpc-issues/`; part of `workflows:check`. |
| `report-failure.sh <job> <run-url>` | | Record a failed run in the drift issue: one dated line per job and day, with the latest run and a count when the job fails again that day. Called from `if: failure()` steps. |
| `drift-issue.sh` | | Shared helpers for the two drift issue scripts (sourced, not run). |
| `start-gateway.sh <core-semver>`, `stop-gateway.sh` | | Run the published gateway container for a core version with the Docker driver and a plaintext endpoint for the E2E lane, and collect its log. |

| Workflow | Script |
|----------|--------|
| `core-watch.yml` (hourly) | `core-tags.sh`, then `open-bump-pr.sh` per tag, then `rpc-issues.sh` and `merge-bump-pr.sh` |
| `release-tag.yml` (push to `main`) | `tag-release.sh` |
| `core-drift.yml` (nightly) | `drift-check.sh`, then `update-drift-issue.sh` |
| `e2e.yml` (reusable) | `start-gateway.sh`, `mise run e2e`, `stop-gateway.sh` |

## RPC coverage issues

`rpc-issues.sh` keeps one GitHub issue (label `rpc-coverage`) per core RPC that end users can call and the SDK does not wrap yet, so every gap has an owner and a ready implementation brief.

- **Selection**: it reads core's protos at the newest core release tag (pre-release or stable, as `core-tags.sh` orders them).
  An RPC is user-facing when its authorization annotation has `auth_mode` `bearer` or `dual`; a method without an annotation counts only in a proto file the gateway serves (one with annotated methods), and its issue says the annotation is missing.
  Plugin and driver protos, and `sandbox`, `peer`, `supervisor`, `operator`, and `unauthenticated` RPCs, never get issues.
- **Coverage**: an RPC is covered when a package under `openshell/v1` (tests and `fake/` excluded) that calls `New<Service>Client(` also calls `.<Method>(` (`rpc-coverage.sh`, also used by `drift-check.sh`).
- **Grouping**: when the SDK wraps none of a service's user-facing methods, one issue tracks the whole service; otherwise each method gets its own issue.
  The hidden marker on the first body line (`<!-- rpc-coverage: kind=... -->`) is the issue's identity; an issue keeps the methods its marker lists, and new methods of a service join its open service issue.
  Only issues the bot opened count, found by creator and marker rather than by label: a marker in any other issue is ignored, and an issue whose `rpc-coverage` label someone removed gets the label back instead of a duplicate.
  Text from core's proto comments is defused before it goes into a body (`@name`, `#123`, and `<!--` get a zero-width space), so it cannot mention people, link issues of this repository, or hide the rest of the body.
- **Lifecycle**: each run opens missing issues, rewrites a body or the `rpc:ready` / `rpc:blocked` label when they change, closes an issue with a reason per method (`covered`, `removed`, `excepted`, `not-user-facing`, or `tracked-elsewhere` when another issue's marker lists the method), and reopens an issue it closed itself when the methods apply again.
  An issue a maintainer closes stays closed (a closed service issue silences the whole service); an issue a maintainer reopens is never closed as `covered` again.
- **Excluding an RPC**: add `Service.Method  reason` to `rpc-exceptions.txt`; the next run closes its issue with the reason.
- **Limit**: a run that would open more than 10 issues fails before any write and lists them.
  If they are expected, run core-watch by hand with the input `max_new_issues`, or locally `mise run core:rpc-issues -- --max-new <n>`.
- **Preview**: `mise run core:rpc-issues -- --dry-run` prints every planned action with its body and changes nothing (it reads GitHub, so `gh` must be logged in).
  Without `--dry-run`, the script applies the plan only with an explicit `BOT_LOGIN` and the bot's token (core-watch passes both); under a person's `gh` login it stops before any write, because later runs would not manage the issues it opened.

## Auto-merge for stable pins

`merge-bump-pr.sh` is off until the repository variable `CORE_AUTO_MERGE` is `on`.
It merges a stable proposal only when the release changes no proto: the regenerated bindings under `proto/` are identical, so the SDK needs no work and can be tagged right away.
A release that changes the protos always waits for review; the SDK release may lag until the new features are wrapped and usable (see the RPC coverage issues).

It merges with the bot token through a ruleset bypass, so an admin has to add the `ai-openshell-sdk-go` App to the `main-integrity` ruleset as a bypass actor with mode "pull requests only".
A proposal goes back to normal review as soon as anything else is pushed to it, a conformance fixture changes, a check fails, or someone adds the `hold` label.
Merges happen on the hourly run after the checks turn green, so a stable SDK release follows the core release within about two hours.
