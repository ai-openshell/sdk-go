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
| `merge-bump-pr.sh [--dry-run] [--auto-merge on\|off]` | `core:merge` | Merge stable proposals that change no proto: one commit with a provenance check from the bump App, exactly the pin bump, green checks, no `hold` label. Off unless `CORE_AUTO_MERGE` is `on`. |
| `pin-bump.sh` | | Sourced by `open-bump-pr.sh` and `merge-bump-pr.sh`: `pin_bump_edit <tag> <kind>` makes the pin and README edits of a bump, `pin_bump_tree <base> <tag> <kind>` prints the tree a bump commit with only those edits has. |
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
| `core-watch.yml` (hourly) | job `watch`: `core-tags.sh`, then `open-bump-pr.sh` per tag, then `rpc-issues.sh`; job `merge` (only when `CORE_AUTO_MERGE` is `on`): `merge-bump-pr.sh` |
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

The proposal must be exactly a pin bump.
`pin_bump_tree` rebuilds the expected tree from the merge base with the same edits `open-bump-pr.sh` makes (the pin in `buf.gen.yaml` and the README line), and the bump commit's tree has to match it.
Any other change sends the proposal to review, including one inside `buf.gen.yaml` such as a plugin entry, a changed conformance fixture, or a second commit.
When auto-merge is on, the bump App records a `core-bump/provenance` check on the stable commit it generated. The merge script verifies the App ID and the exact PR head SHA. A new head without that check goes through review, including proposals opened before auto-merge was enabled.
The proposal also goes back to review when a check fails or someone adds the `hold` label.
It waits for review, too, when its tag is not newer than the pin on `main`, or when `main` moved the pin since the proposal was cut (after an earlier proposal merged, a second one from the same base would only conflict).
If GitHub refuses a merge, the script leaves that proposal for review, goes on with the others, and fails the run so the failure is reported; the comment explaining an unreviewed merge is posted only after the merge succeeds.
Merges happen on the hourly run after the checks turn green, so a stable SDK release follows the core release within about two hours.

The merge runs in its own core-watch job, `merge`, with the token of a second GitHub App that only merges (secrets `SDK_MERGE_CLIENT_ID` and `SDK_MERGE_PRIVATE_KEY` in `sdk-bot`).
That job runs no generator or other code from core.
The script checks the proposal; who may write a `core/*` branch and who may skip review are set by repository rules.
Before an admin sets `CORE_AUTO_MERGE` to `on`:

1. A ruleset on `core/*` lets only the `ai-openshell-sdk-go` App and maintainers create, update, or delete those branches.
2. A ruleset on `main` requires the GitHub Actions `ci` and `e2e-gate` checks with no bypass actors, and the separate DCO rule stays in force. Auto-merge also requires an actual `e2e / e2e` pass on the proposal.
3. The bump App installation has checks write so `core-watch` can record provenance when auto-merge is enabled.
4. The merge App is installed on this repository with contents and pull requests write, checks and statuses read, and issues write (for failure reports), and its secrets are in `sdk-bot`.
5. The merge App is a PR-only bypass actor for `main-integrity` on this repository, so its merge can skip the code owner review.
