# Tasks: Effective Go SDK Client Configuration

**Input**: [spec.md](spec.md), [plan.md](plan.md), [research.md](research.md), [contracts/client-policy.md](contracts/client-policy.md)
**Tests**: Required by FR-013. Write the relevant checks first, observe failure, then implement the same task's behavior. Every task ends with passing tests.
**Global constraints**: Every task inherits plan.md's Global Constraints. No public signature removals, dependencies, generated binding changes, core edits, or payload/error leakage.

## Phase 1: Foundation

- [ ] T001 Implement validated immutable client policy construction in `openshell/v1/internal/grpc/policy.go` and `policy_test.go`, including negative/reversed validation, zero defaults, zero retry count, snapshot behavior and safe duration bounds; pin defaults with `TestClientOptions_Validation` and `TestClientOptions_Snapshot`.

**Interfaces**: `ClientOptions(timeout time.Duration, retry *types.RetryPolicy, logger types.Logger) ([]grpc.DialOption, error)` returns SDK invalid-argument errors for negative fields or effective maximum below initial; copies policy and defaults waits to 100 ms/5 s. Configure gRPC service-config retry disablement through returned options.
**Acceptance**: `MaxRetries=0` schedules no retries; invalid policy still fails; caller struct remains unchanged. Run full `mise exec -- go test ./...` after completion.

**Pinned assertions**: Validation tables include negative timeout/count/each wait, zero waits, and bounds reversed only after defaulting. Assert `assert.True(t, types.IsInvalidArgument(err))` for invalid settings and `assert.Equal(t, original, *callerPolicy)` after construction. Default normalization must select exactly `100*time.Millisecond` and `5*time.Second`. Snapshot behavior may inspect normalized internal state here; T003 proves it through actual retries.

## Phase 2: User Story 1 — Bound Unary Requests

- [ ] T002 [US1] Wire the shared unary policy through `openshell/v1/internal/grpc/conn.go` and `openshell/v1/client.go`; implement deadline/cancellation behavior and add `openshell/v1/client_policy_test.go` behavior cases for default deadlines, explicit shorter/longer deadlines, already-canceled contexts, and preserved watch/exec/polling lifetimes (`TestClientPolicy_Timeout`, `TestClientPolicy_CallerDeadline`, `TestClientPolicy_StreamLifetime`, `TestClientPolicy_PollingLifetime`).

**Interfaces**: Consume T001 `ClientOptions`; extend only internal `NewConnection` to `NewConnection(address string, tlsCfg *TLSParams, auth credentials.PerRPCCredentials, options ...grpc.DialOption) (*grpc.ClientConn, error)`. Existing three-argument callers still compile.
**Acceptance**: Delayed unary handler returns typed deadline error; explicit longer context allows completion beyond default; stream can outlive the default until cancellation; promptly responding polling can outlive the default. Zero timeout leaves no injected deadline. Context failures retain SDK typed conversion. Run full tests, then correctness checkpoint 1.

**Pinned assertions**: In controlled invokers, use `assert.WithinDuration(t, started.Add(configuredTimeout), actualDeadline, timingTolerance)` and `assert.Equal(t, callerDeadline, actualDeadline)` for both shorter and longer explicit deadlines; zero timeout requires `assert.False(t, hasDeadline)`. A context canceled before entry requires `assert.Zero(t, invocations)`. Cancel an active unary call and each surviving stream/poll after checking it outlives the default; verify the caller's context itself remains usable after an SDK-created deadline expires. Use synchronization and declared conservative timing tolerances in real transport cases.

## Phase 3: User Story 2 — Safe Read Retries

- [ ] T003 [US2] Implement the explicit 22-method allowlist from `research.md` and cancelable bounded retry loop in `openshell/v1/internal/grpc/policy.go`; add `policy_test.go` and public-constructor cases for recovery, exhaustion, delay sequence/cap, a single deadline budget, cancellation during backoff, concurrent independent calls, and no retries for unsafe/unknown/stream methods or non-Unavailable status (`TestClientPolicy_Retry`, `TestClientPolicy_RetryExclusions`, `TestClientPolicy_RetryBudget`, `TestClientPolicy_Backoff`).

**Interfaces**: Reuse T001/T002 policy and connection options. Eligibility uses exact generated full-method constants; inspect `GetSandboxProviderStatus` and credential exclusions explicitly. Retry count means additional attempts; delay is initial then doubled and safely capped. No service-config application retries may bypass this classification.
**Acceptance**: Handler attempts are bounded by 1 + MaxRetries, no original `ResourceExhausted` retry, no mutation/execution/session replay, context cancellation interrupts long backoff, copied policy governs later calls, very large durations do not overflow. Run full tests.

**Pinned assertions**: Table every one of the 22 allowlisted methods against a controlled `Unavailable` then success invoker: `assert.Equal(t, 2, attempts)`. Table all other generated unary methods and an unknown method against persistent `Unavailable`: `assert.Equal(t, 1, attempts)`. With `MaxRetries=2`, exhaustion requires `assert.Equal(t, 3, attempts)`; nil/zero policies and other statuses require one. For initial `10*time.Millisecond` and cap `25*time.Millisecond`, assert delays equal `[]time.Duration{10*time.Millisecond, 20*time.Millisecond, 25*time.Millisecond, 25*time.Millisecond}`. Verify cancel/deadline during backoff prevents the next invocation; a huge retry count remains cancelable without counter overflow, and maximum duration values saturate without wrapping or a busy loop. A transport fixture with an explicit service-config retry policy must still observe exactly one unsafe application invocation. T002's stream fixture also returns `Unavailable` to prove no reconnect/replay.

## Phase 4: User Story 3 — Sanitized Diagnostics

- [ ] T004 [US3] Add logical unary completion/retry diagnostics in `openshell/v1/internal/grpc/policy.go`; capture every logger argument in `policy_test.go` and `client_policy_test.go` to verify method/duration/status/attempt/delay metadata, Debug success/retry events, Error failure events, nil behavior, and exclusion of synthetic request/response/auth/server-error secrets (`TestClientPolicy_Logging`, `TestClientPolicy_LogSanitization`).

**Interfaces**: Reuse existing `types.Logger` and T001/T003 policy; failure logs receive only a newly constructed status-code error, never the original error object. One completion per logical call records aggregate attempts/duration; no stream lifecycle logging guarantee is added.
**Acceptance**: Logger works when retry policy is nil, total attempt count matches handler invocations, all logger message/field/error arguments exclude seeded secrets, and concurrent requests are race-free with a synchronized test logger. Run full tests, then correctness checkpoint 2.

**Pinned assertions**: A three-attempt call has `assert.Len(t, completions, 1)` and `assert.Len(t, scheduledRetries, 2)`; the completion reports three invocations and total elapsed time. Successful completion/retries use Debug, final failure uses Error, and nil policy still emits one completion. Assert absence of each synthetic secret from recursively captured fields and formatted error arguments with `assert.NotContains(t, capturedOutput, secret)`; check both successful responses and failed calls, including a pre-canceled call with zero attempts. Nil logger must execute without a diagnostic sink.

## Phase 5: Constructor Parity

- [ ] T005 [US1] Forward all values consistently in `openshell/v1/gateway/gateway.go` and add `openshell/v1/gateway/client_policy_test.go` table-driven public direct/gateway parity tests for timeout, retries, logger delivery, nil/zero values, and invalid negative/reversed inputs (`TestClientPolicy_ConstructorParity`); this shared integration task also covers US2 and US3.

**Interfaces**: Existing `gateway.WithTimeout`, `WithRetryPolicy`, `WithLogger` feed the common T001 constructor path. Do not silently discard negative timeout values before validation.
**Acceptance**: Both constructor paths observe the same health timeout/retry/logger behavior and invalid-argument failures. Run full tests.

**Pinned assertions**: Execute the same cases through both constructors: default/shorter/longer caller deadlines, a two-attempt read recovery, one logger completion without retry policy, zero/nil behavior, and every T001 invalid input returning `types.IsInvalidArgument(err)`. Use valid temporary gateway metadata and explicit test auth so discovery/auth failures cannot hide configuration results.

## Phase 6: Public Contract and Validation

- [ ] T006 Update comments in `openshell/v1/types/config.go`, `types/types.go`, `types/logger.go`, `gateway/options.go` and docs in `docs/src/api/client.md`, `docs/src/api/gateway.md`; run `mise run ci` and `mise run docs:build`, resolving any failures introduced by this feature and recording final evidence in `quickstart.md`.

**Interfaces**: Existing public types/options only; contract is fixed by spec and `contracts/client-policy.md`.
**Acceptance**: Document explicit caller-deadline precedence, unary versus operation lifetime, 100 ms/5 s defaults, retry count/cap/eligibility, invalid settings, nil logger, concurrent logger expectations, safe diagnostics and transparent transport retry distinction. Formatting, lint, build, race-enabled tests, pinned proto, conformance, docs coverage and docs build pass. Update this file's checkboxes with actual completion.

## Dependencies and Execution

T001 → T002 → T003 → T004 → T005 → T006. These tasks touch one shared connection policy and run sequentially. No parallel implementation markers are appropriate. Within each story, independent test scenarios may be authored together, but do not concurrently edit shared policy/test files.

Deliver the timeout behavior first as the smallest useful increment, then add safe retries, diagnostics, constructor parity, and documentation. Do not push or merge; implementation skips all after_implement hooks because ship runs review-code separately.

## Requirement Coverage

| Requirements | Tasks |
|--------------|-------|
| FR-001 | T001, T002, T005, T006 |
| FR-002–004 | T002, T003, T005, T006 |
| FR-005–008 | T001, T003, T005, T006 |
| FR-009–010, FR-016 | T004, T005, T006 |
| FR-011 | T005 |
| FR-012 | T006 |
| FR-013 | T001–T006 |
| FR-014 | T001, T005, T006 |
| FR-015 | T003, T006 |
| SC-001 | T002, T005: controlled deadline values and real delayed unary behavior |
| SC-002 | T003: complete eligible/ineligible method tables, retry limits and delay sequence |
| SC-003 | T002, T003: active cancellation, interruptible backoff and shared deadline |
| SC-004 | T004, T005: captured completion/retry metadata and secret exclusions |
| SC-005 | T006: race-enabled full CI and docs build; unchanged public signatures |
