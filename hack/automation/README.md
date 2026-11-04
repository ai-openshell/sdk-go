# Automation scripts

Every workflow under `.github/workflows/` wraps one of these scripts through a mise task, so each job can be run locally before it runs in CI.
Scripts that write to GitHub take `--dry-run` and print what they would do instead.
Workflows run them with a token of the `ai-openshell-go-sdk` GitHub App (`ai-openshell-go-sdk[bot]`; secrets `SDK_BOT_CLIENT_ID` and `SDK_BOT_PRIVATE_KEY` in the `sdk-bot` environment, which only `main` can deploy to), never with `GITHUB_TOKEN`, so their pushes and PRs trigger CI.

| File | mise task | Purpose |
|------|-----------|---------|
| `core-tags.sh` | `core:tags` | List NVIDIA/OpenShell release tags newer than the pin (`buf.gen.yaml`, `inputs[0].tag`), semver-sorted with pre-releases below their stable version. |
| `open-bump-pr.sh <tag> [--dry-run]` | `core:bump` | Open the update proposal for one core tag: branch `core/<tag>`, pin change, regenerated bindings, proto diff in the body, labels `core-bump` plus `stable` or `prerelease`. Closes older pre-release proposals. |
| `bump-pr-body.md` | | PR body template for `open-bump-pr.sh`. |
| `tag-release.sh [--dry-run]` | `release:tag` | After a stable pin lands on `main`: create the lockstep tag `vX.Y.Z` and the GitHub release, then ask the Go module proxy for the version. |
| `drift-check.sh --out <file>` | `core:drift` | Compare core `main` against the pin: `buf breaking`, regenerate and build, uncovered RPCs. Writes the markdown report. |
| `update-drift-issue.sh <report.md>` | | Create, update, or close the single `Core main drift` issue from a report. |
| `report-failure.sh <job> <run-url>` | | Append a dated failure line to the drift issue. Called from `if: failure()` steps. |
| `drift-issue.sh` | | Shared helpers for the two drift issue scripts (sourced, not run). |
| `start-gateway.sh <core-semver>`, `stop-gateway.sh` | | Run the published gateway container for a core version with the Docker driver and a plaintext endpoint for the E2E lane, and collect its log. |
| `adapt-context.sh <pr-or-issue-number>` | | Build the `## Adaptation task` comment for the agent workflow: pin diff, proto diff, failed log tail, uncovered RPCs. |
| `adapt-task-body.md` | | Comment template for `adapt-context.sh`. |

| Workflow | Script |
|----------|--------|
| `core-watch.yml` (hourly) | `core-tags.sh`, then `open-bump-pr.sh` per tag |
| `release-tag.yml` (push to `main`) | `tag-release.sh` |
| `core-drift.yml` (nightly) | `drift-check.sh`, then `update-drift-issue.sh` |
| `e2e.yml` (reusable) | `start-gateway.sh`, `mise run e2e`, `stop-gateway.sh` |
| `adapt.yml` (label `agent:adapt`) | `adapt-context.sh` |
