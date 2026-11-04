# Contributing to the OpenShell Go SDK

## Setup

Install [mise](https://mise.jdx.dev/), then install the pinned tools:

```shell
mise install --locked
```

`mise tasks` lists every task. The ones you need most:

| Task | What it does |
|------|--------------|
| `mise run ci` | Everything CI runs: format check, lint, build, tests, proto check, conformance check, docs check |
| `mise run fmt` | Format the code |
| `mise run test` | Unit tests with the race detector |
| `mise run proto:gen` | Regenerate the bindings from core at the pinned tag |
| `mise run core:version` | Print the pinned core version |
| `mise run workflows:check` | Lint the GitHub workflows |
| `mise run docs:build` | Build the docs into `docs/book` |

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>): <description>`.
- Sign off every commit (`git commit --signoff`) for the [DCO](DCO).
- Every change goes through a pull request with an approval from a code owner who did not push last.

## Bindings and core versions

The bindings in `proto/` are generated from core's `proto/` at one core release tag, the `tag:` line in `buf.gen.yaml`. Don't edit generated files; change the pin and run `mise run proto:gen`. `mise run proto:check` fails when the committed bindings don't match the pin.

Shared test fixtures come from core's `sdk/conformance/` and are vendored next to the tests that read them. `mise run conformance:check` compares them with core at the pinned tag.

SDK releases follow core releases: SDK `vX.Y.Z` is built from core `vX.Y.Z`, and there are no SDK-only releases.

## Core bumps and drift

Automation watches core for new tags and opens a pull request that moves the pin, labeled `core-bump` plus `stable` or `prerelease`. If a bump needs SDK changes, push them to the same branch. Merging a bump to a stable tag tags the SDK release.

A nightly job checks core's `main` for breaking proto changes and RPCs the SDK doesn't cover yet, and keeps one issue labeled `core-drift` up to date.
