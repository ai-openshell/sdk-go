# Implementation Plan: Effective Go SDK Client Configuration

**Branch**: `001-go-sdk-client-config` | **Date**: 2026-11-06 | **Spec**: [spec.md](spec.md)

## Summary

Validate and snapshot existing client settings, then apply them through one unary RPC interceptor on the shared connection. All attempts share one deadline; only verified safe reads retry; diagnostics contain no application data. Gateway construction forwards values to the same validation. Streaming lifetimes remain caller-controlled.

## Technical Context

- Go 1.25.14 via mise, existing gRPC v1.83.2 and standard library; no dependencies added.
- Concurrent client library; no storage, server, proto, auth or TLS behavior changes.
- Existing testify tests plus controlled invokers and loopback gRPC server fixtures.
- Baseline `mise exec -- go test ./...` passed before implementation.
- Avoid payload formatting and shared per-call state. No wait occurs except configured retry backoff. An interceptor may be omitted when every setting is inactive.

## Constitution Check

The installed constitution is an unfilled template, not adopted policy. Existing source compatibility, scope, signed-off Conventional Commits, and no-secret logging rules apply. Pre- and post-design check: pass; no public signature removals, dependency additions or core changes.

## Global Constraints

- Preserve the existing `Config` fields, `RetryPolicy`, logger interface, and gateway option constructors.
- Apply a positive timeout as a default deadline to every unary request with no caller deadline. Zero supplies no default.
- Never apply configured retries to mutations, execution, session creation, streams, or unclassified methods.
- Zero waits default to 100 ms initial and 5 s maximum; zero retry count disables additional attempts. Reject negative values and effective `MaxWait < InitialWait` with `ErrorInvalidArgument`.
- Backoff doubles with safe saturation at `MaxWait`; no jitter or server hints in this version.
- Do not log request/response bodies, authentication metadata, credentials, raw server error text, or secret-bearing error objects.
- Use repository mise tasks. Do not modify the frozen monorepo SDK.

## File Structure and Interfaces

| File | Responsibility |
|------|----------------|
| `openshell/v1/internal/grpc/policy.go` (new) | Validate/copy settings, classify safe reads, unary deadline/retry/logger behavior |
| `openshell/v1/internal/grpc/policy_test.go` (new) | Controlled attempt/delay/cancel/snapshot/overflow/sanitization checks |
| `openshell/v1/internal/grpc/conn.go` | Accept internal additional dial options, retain auth/TLS setup |
| `openshell/v1/client.go` | Build validated options and wire shared connection |
| `openshell/v1/client_policy_test.go` (new) | Public constructor timeout/caller-context and stream/polling behavior |
| `openshell/v1/gateway/gateway.go` | Forward negative/zero values to common validation |
| `openshell/v1/gateway/client_policy_test.go` (new) | Direct/gateway behavior parity, all three settings and validation |
| `openshell/v1/types/config.go`, `types/types.go`, `types/logger.go`, `gateway/options.go` | Public behavior/default/validation/concurrency comments |
| `docs/src/api/client.md`, `docs/src/api/gateway.md` | Consumer semantics and examples, replacing conflicting timeout claims |

Internal interfaces for dependent tasks:

- `internal/grpc.ClientOptions(timeout time.Duration, retry *types.RetryPolicy, logger types.Logger) ([]grpc.DialOption, error)`: validate, copy/default policy; return `types.StatusError` with invalid-argument code for invalid values. Do not import `v1` into its internal transport package.
- `internal/grpc.NewConnection(address string, tlsCfg *TLSParams, auth credentials.PerRPCCredentials, options ...grpc.DialOption) (*grpc.ClientConn, error)`: variadic internal extension preserving existing callers.
- Public `v1.NewClient` and `gateway.NewClient` signatures remain unchanged. Private policy method names may remain implementation-local.

## Design

1. Normalize a copied retry value and validate even when retries are zero. Install `grpc.WithDisableRetry()` after any provided dial options to block service-config retries while retaining documented pre-application transparent retries. Do not disable unrelated service config features.
2. Use one unary interceptor. Derive a timeout only if the caller has no deadline, defer its cancel, and use that context for every attempt/wait. Check context before attempts. Map context failures to matching gRPC status for existing SDK conversion.
3. Exact generated full-method constants identify the 22 verified reads in [research.md](research.md). Unknown, state-mutating getters, credential resolution and uncertain extension snapshot calls remain ineligible. Retry only original `codes.Unavailable`, not normalized SDK error categories.
4. Keep retry counters/timers local. Use cancelable waits and guard duration multiplication. One completion diagnostic records full method, total duration, final status and total attempts; retry diagnostics record method, attempt and delay. Never pass the original error to the logger: construct a synthetic status-only error on failures.
5. No stream interceptor changes lifetime or retries. Unary setup calls still follow defaults; polling overall lifetime and `Exec.Run` remain controlled by caller contexts.

## Validation and Review Focus

Write failing behavior checks before implementation. Use controlled invokers for exact attempt limits and conservative timing bounds for real gRPC tests.

- Cover no deadline, explicit shorter/longer deadlines, cancellation before a call and during backoff, shared retry budget, zero/nil settings, defaults, negative/reversed settings, and duration saturation.
- Cover eligible recovery/exhaustion, mutations, unknown methods, streams, `ResourceExhausted`, and excluded state-mutating status getters.
- Mutate caller policy after construction to prove snapshot behavior; check concurrent independent calls under the race detector.
- Capture all logger arguments including error objects; put synthetic secrets in request, response, auth and server errors. Completion logs must work with nil retry policy.
- Prove streams survive beyond default timeout and prompt polling can outlive individual default budgets; explicitly cancel all goroutines/streams.
- Run `mise run test`, `mise run format:check ::: lint ::: build ::: docs:check`, and `mise run docs:build`. Finish branch checks with `mise run ci` including pinned proto/conformance checks. No live sandbox infrastructure is changed.

## Delivery

One feature branch through spec, plan and code reviews. Keep installed workflow setup and ephemeral state out of product commits. Do not push, merge, publish or open a PR before the post-pipeline completion choice.
