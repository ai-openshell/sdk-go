# Automation scripts

Every workflow under `.github/workflows/` wraps one of these scripts through a mise task, so each job can be run locally before it runs in CI.
Scripts that write to GitHub take `--dry-run` and print what they would do instead.
Workflows run them with a token of the `ai-openshell-sdk-go` GitHub App (`ai-openshell-sdk-go[bot]`; secrets `SDK_BOT_CLIENT_ID` and `SDK_BOT_PRIVATE_KEY` in the `sdk-bot` environment, which only `main` can deploy to), never with `GITHUB_TOKEN`, so their pushes and PRs trigger CI.

| File | mise task | Purpose |
|------|-----------|---------|
| `core-tags.sh` | `core:tags` | List NVIDIA/OpenShell release tags newer than the pin (`buf.gen.yaml`, `inputs[0].tag`), semver-sorted with pre-releases below their stable version. |
| `open-bump-pr.sh <tag> [--dry-run]` | `core:bump` | Open the update proposal for one core tag: branch `core/<tag>`, pin change, regenerated bindings, proto diff in the body, labels `core-bump` plus `stable` or `prerelease`. Closes older pre-release proposals. |
| `bump-pr-body.md` | | PR body template for `open-bump-pr.sh`. |
| `merge-bump-pr.sh [--dry-run] [--auto-merge on\|off]` | `core:merge` | Merge stable proposals that change no proto: one bot commit touching only the pin and the README (nothing under `proto/`), green checks, no `hold` label. Off unless `CORE_AUTO_MERGE` is `on`. |
| `tag-release.sh [--dry-run]` | `release:tag` | After a stable pin lands on `main`: create the lockstep tag `vX.Y.Z` and the GitHub release, then ask the Go module proxy for the version. |
| `drift-check.sh --out <file>` | `core:drift` | Compare core `main` against the pin: `buf breaking`, regenerate and build, uncovered RPCs. Writes the markdown report. |
| `update-drift-issue.sh <report.md>` | | Create, update, or close the single `Core main drift` issue from a report. |
| `report-failure.sh <job> <run-url>` | | Append a dated failure line to the drift issue. Called from `if: failure()` steps. |
| `drift-issue.sh` | | Shared helpers for the two drift issue scripts (sourced, not run). |
| `start-gateway.sh <core-semver>`, `stop-gateway.sh` | | Run the published gateway container for a core version with the Docker driver and a plaintext endpoint for the E2E lane, and collect its log. |

| Workflow | Script |
|----------|--------|
| `core-watch.yml` (hourly) | `core-tags.sh`, then `open-bump-pr.sh` per tag, then `merge-bump-pr.sh` |
| `release-tag.yml` (push to `main`) | `tag-release.sh` |
| `core-drift.yml` (nightly) | `drift-check.sh`, then `update-drift-issue.sh` |
| `e2e.yml` (reusable) | `start-gateway.sh`, `mise run e2e`, `stop-gateway.sh` |

## Auto-merge for stable pins

`merge-bump-pr.sh` is off until the repository variable `CORE_AUTO_MERGE` is `on`.
It merges a stable proposal only when the release changes no proto: the regenerated bindings under `proto/` are identical, so the SDK needs no work and can be tagged right away.
A release that changes the protos always waits for review; the SDK release may lag until the new features are wrapped and usable (see the RPC coverage issues).

It merges with the bot token through a ruleset bypass, so an admin has to add the `ai-openshell-sdk-go` App to the `main-integrity` ruleset as a bypass actor with mode "pull requests only".
A proposal goes back to normal review as soon as anything else is pushed to it, a conformance fixture changes, a check fails, or someone adds the `hold` label.
Merges happen on the hourly run after the checks turn green, so a stable SDK release follows the core release within about two hours.
