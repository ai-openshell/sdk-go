# Agent Instructions

The OpenShell Go SDK: module `github.com/ai-openshell/sdk-go`, a client for the OpenShell gateway. See [CONTRIBUTING.md](CONTRIBUTING.md) for tasks and the release model.

## Commands

- `mise run ci` runs the full pipeline (format check, lint, build, test, proto check, conformance check, docs check).
- `mise run proto:gen` regenerates `proto/` from core at the tag pinned in `buf.gen.yaml`. Never edit generated files by hand.

## Code conventions

- Domain types in `openshell/v1/types/` must not import proto packages.
- Converters in `openshell/v1/internal/converter/` deep-copy slices and maps at boundaries.
- Tests use bufconn for in-process gRPC and testify for assertions.
- Test fixtures live in the `testdata/` directory of the package that reads them. Never read files outside the module.

## Commits

- Use Conventional Commits and sign off every commit (`--signoff`).
- Never mention Claude or any AI agent in commits, pull request descriptions, or issues (no author attribution, no Co-Authored-By, no "Generated with" footers).
