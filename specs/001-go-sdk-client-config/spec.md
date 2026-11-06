# Feature Specification: Effective Go SDK Client Configuration

**Feature Branch**: `001-go-sdk-client-config`
**Created**: 2026-11-06
**Status**: Draft
**Source Issue**: https://github.com/ai-openshell/sdk-go/issues/20
**Input**: Agreed approach A in the Go SDK client configuration brainstorm: preserve all three public settings and implement conservative, documented behavior.

## Overview

SDK consumers currently configure timeout, retry, and logger settings that silently do nothing. This change makes those settings effective while preserving caller-controlled waits and sessions, preventing configured replay of side effects, and keeping application data out of diagnostics.

## User Scenarios & Testing

### User Story 1 - Bound individual requests (Priority: P1)

An SDK consumer configures a default timeout so an unresponsive gateway cannot block an individual unary request indefinitely, while retaining explicit control of long waits and sessions through contexts.

**Why this priority**: Silent timeout failure can hang consumers indefinitely.
**Independent Test**: Call a delayed health handler through a client configured with a short timeout, then repeat with explicit shorter and longer caller deadlines.

**Acceptance Scenarios**:

1. **Given** a positive timeout and a context with no deadline, **When** a unary request stalls, **Then** it ends with the SDK deadline error near the configured duration.
2. **Given** a context with an explicit deadline, **When** a request is made, **Then** that deadline is authoritative even when longer than the configured default, and cancellation still interrupts the request.
3. **Given** a default timeout and retries, **When** the request fails transiently, **Then** all its attempts and backoff share one deadline budget.
4. **Given** a watch, exec stream, or tunnel, **When** it remains active beyond the default timeout, **Then** the timeout does not end its lifetime; caller cancellation still does.
5. **Given** a polling helper such as `WaitReady`, **When** its individual requests complete promptly, **Then** the overall wait may exceed the default timeout and ends according to its caller context.

### User Story 2 - Retry safe reads predictably (Priority: P2)

An SDK consumer opts into retrying safe unary reads during transient gateway unavailability without risking duplicate mutations, sessions, or executed commands.

**Why this priority**: Automatic replay can duplicate side effects if it is too broad.
**Independent Test**: A read handler returns `Unavailable` before succeeding; an unsafe handler returns the same status and receives no SDK-configured retries.

**Acceptance Scenarios**:

1. **Given** an eligible read and a configured retry policy, **When** the server returns `Unavailable`, **Then** the SDK retries with bounded backoff until success, exhaustion, cancellation, or the request deadline.
2. **Given** a mutation, session creation, command execution, or streaming operation, **When** it fails, **Then** the configured retry policy does not replay it.
3. **Given** a safe read returning another status, including `ResourceExhausted`, **When** it fails, **Then** the configured policy does not retry it even if the SDK maps that status to its unavailable error category.
4. **Given** no policy or zero permitted retries, **When** a server returns a retryable failure, **Then** the SDK does not schedule retries.

### User Story 3 - Receive useful, sanitized diagnostics (Priority: P2)

An SDK consumer provides the existing logger to observe request timing, outcomes, and configured retries without exposing credentials or application data.

**Why this priority**: A logger that is silently ignored prevents troubleshooting; unsafe diagnostics expose secrets.
**Independent Test**: Capture logs from successful and failed requests and verify expected metadata and absence of synthetic secrets in request bodies and server error text.

**Acceptance Scenarios**:

1. **Given** a custom logger, **When** a unary request completes, **Then** it receives method, duration, and status diagnostics, including when retries are disabled.
2. **Given** a request that schedules a retry, **When** that retry is scheduled, **Then** diagnostics identify the method, attempt, and delay.
3. **Given** sensitive data in request/response bodies, authentication, or server errors, **When** diagnostics are emitted, **Then** none of that data appears in message text, structured fields, or logger error arguments.
4. **Given** a nil logger, **When** requests execute, **Then** the SDK emits no logs through this configuration facility.

### Edge Cases

- Zero timeout preserves the absence of a default; negative timeout fails construction with `ErrorInvalidArgument` through either constructor.
- A zero retry count permits no additional attempts; zero wait fields select defaults of 100 ms initial and 5 s maximum. Negative policy fields or a maximum below the initial wait after defaults fail construction with `ErrorInvalidArgument`.
- Large retry limits and duration values must not overflow backoff calculations or produce a busy loop.
- A context canceled before a call must not cause new retries; cancellation during backoff must interrupt the wait promptly.
- Concurrent requests must not share mutable attempt counters or modify caller-owned policy values.
- Explicitly configured transport retry behavior must not broaden the safe-read policy; gRPC transparent retries require a documented distinction.
- `Exec.Run` consumes a stream even though it returns one final result; the default unary timeout does not bound the entire command.
- Unary setup requests for streaming/session operations still receive unary defaults when they have no explicit deadline.

## Requirements

### Functional Requirements

- **FR-001**: Preserve the existing `Config` fields, `RetryPolicy`, logger interface, and gateway option constructors.
- **FR-002**: Apply a positive timeout as a default deadline to every unary request with no caller deadline. Zero supplies no default.
- **FR-003**: Respect explicit caller deadlines, shorter or longer than the default, and caller cancellation. One unary request budget includes every configured retry and backoff.
- **FR-004**: Do not impose a default lifetime limit on streams, exec streams, watches, or tunnels. Polling and multi-request helpers use caller contexts for their total duration; their individual unary requests follow FR-002/003.
- **FR-005**: A nil retry policy means no SDK-configured retries. A configured policy retries only explicitly classified safe read unary methods and only on original gRPC `Unavailable` status.
- **FR-006**: Never apply configured retries to mutations, execution, session creation, streams, or unclassified methods.
- **FR-007**: Bound retries and delays using the existing policy fields. Stop on exhaustion, non-retryable status, cancellation, or the shared deadline; interrupt backoff on cancellation/deadline.
- **FR-008**: Safely snapshot policy configuration at client construction so concurrent requests do not mutate or race on caller-owned policy state.
- **FR-009**: When a logger is configured, provide unary completion and retry diagnostics with method, duration/status, and attempt/delay metadata as applicable. Do not require a retry policy to receive completion diagnostics.
- **FR-010**: Do not log request/response bodies, authentication metadata, credentials, raw server error text, or secret-bearing error objects. Nil logger means no SDK-configured log output.
- **FR-011**: Direct and gateway constructors must have identical configuration behavior, including validation and zero values.
- **FR-012**: Document the timeout/RPC boundary, caller-deadline precedence, retry eligibility and bounds, logging behavior, defaults, and validation on the existing public types/options and relevant SDK docs pages.
- **FR-013**: Behavioral tests must cover all three settings, constructor parity, zero/nil settings, invalid settings, explicit deadlines, cancellation, bounded retry attempts/delays, retry exclusions, stream lifetime preservation, and diagnostic sanitization.
- **FR-014**: Reject negative timeout, retry count, and retry waits, and reject effective `MaxWait < InitialWait`, using the SDK invalid-argument error. Treat `MaxRetries` as additional attempts after the initial call, with zero disabling configured retries. Default zero `InitialWait` to 100 ms and zero `MaxWait` to 5 s; validate bounds after defaulting without mutating caller input.
- **FR-015**: Wait `min(InitialWait * 2^(retry-1), MaxWait)` before retry number `retry`, saturating safely to avoid overflow. This version adds no jitter and does not apply server retry hints. Cancellation/deadline always interrupt the wait.
- **FR-016**: Emit one completion diagnostic for each logical unary RPC, with total duration, final status, and attempt count. Use Debug for successful completions and scheduled retries, Error for failed completions with only a sanitized status error. Streaming lifecycle diagnostics and global gRPC logging are outside this logger contract. Logger implementations must support concurrent calls.

### Key Entities

- **Client configuration**: Connection settings plus per-request timeout defaults, optional retry policy, and optional logger.
- **Retry policy**: Additional-attempt limit and initial/maximum backoff durations, independent for each eligible request.
- **Request budget**: One unary request's governing deadline and cancellation, including all its attempts and waits.
- **RPC diagnostic**: Sanitized method/outcome/timing or retry metadata, containing no application data.

## Success Criteria

### Measurable Outcomes

- **SC-001**: A stalled request with a configured default ends near its deadline; explicit shorter and longer deadlines are honored in automated behavior checks through both constructors.
- **SC-002**: Every classified read follows configured attempt and delay bounds; zero configured replays occur for unsafe, streaming, or unclassified methods.
- **SC-003**: Caller cancellation promptly terminates active requests and backoff, and retries never extend the governing deadline.
- **SC-004**: Configured loggers receive completion and retry diagnostics while no synthetic request, credential, or server-error secrets appear in captured output.
- **SC-005**: All existing unit tests, race checks, formatting, lint, build, and relevant documentation checks pass; public configuration signatures remain source-compatible.

## Assumptions

- This change targets the standalone `ai-openshell/sdk-go` repository, not the frozen monorepo copy.
- Existing gRPC and standard library facilities suffice; no new logging framework or retry dependency is required.
- Source compatibility is preserved. The intentionally observable change is that previously ignored configuration becomes effective.
- Exact safe-read RPC classification is derived from existing methods during planning. Unclassified methods remain ineligible.

## Clarifications

### Session 2026-11-06

These choices were selected autonomously under ship's smart oversight; the user's accepted timeout and retry scope remain unchanged.

- Q: What do invalid and zero configuration values mean? → A: Reject negative values and reversed effective wait bounds; zero timeout disables the default, zero retries disables configured retries, and zero waits default to 100 ms/5 s.
- Q: Does `MaxRetries` count attempts or retries? → A: It counts additional attempts, so total handler invocations are bounded by one plus `MaxRetries`, apart from transport-transparent retries before server application processing.
- Q: What backoff and server hint behavior apply? → A: Saturating exponential backoff from the initial wait to the maximum, with no jitter or server hints in this version.
- Q: Which log events and levels are guaranteed? → A: One logical unary completion with timing/status/attempt count; Debug on success and retries, Error on failure with a sanitized status error; no stream lifecycle logging guarantee.
- Q: How do retries interact with gRPC transport features? → A: Do not install service-config retries or retry streams; disable externally supplied service-config retry behavior for this connection so it cannot broaden the explicit safe-read policy. gRPC's pre-application transparent retries remain a transport behavior, not configured application replay.

## Out of Scope

- New whole-operation timeout APIs or session lifetime limits.
- Stream reconnection/resume and automatic mutation retries.
- Payload logging, new logging frameworks, or changes to auth/TLS behavior.
- SDK migration, core gateway changes, or protobuf changes.
