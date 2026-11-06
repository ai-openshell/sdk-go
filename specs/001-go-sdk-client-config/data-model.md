# Client Policy Data Model

Existing public Config and RetryPolicy shapes remain unchanged.

## Normalized Policy

- Nonnegative timeout; zero disables the unary default.
- Optional immutable retry value with nonnegative additional-attempt count and positive ordered waits after zero defaults (100 ms/5 s).
- Optional existing Logger, safe for concurrent calls.
- Closed set of 22 eligible generated unary method names; unknown names are ineligible.

## Per-Call State

Each logical unary call owns its effective context/cancel function, attempt count, retries remaining, next capped delay, start time and final status. Transition: attempt → completion, or eligible failure → cancelable wait → next attempt. Cancellation, deadline, exhaustion and other statuses transition to completion. No per-call state is shared.

## Diagnostics

Completion: fixed message, method, elapsed duration, status code, total attempts. Retry: fixed message, method, attempt, delay. Failure error objects contain only canonical status code, not the original error or details. No bodies, auth metadata or context values enter diagnostics.
