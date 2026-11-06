---
runbook_version: 1
feature: 001-go-sdk-client-config
status: drafted
mode: light
steps_total: 3
step_results: pending,pending,pending
drafted_at: 1ac9f123
drafted_fingerprint: 9a1aaf82d9578b5e8614bfda64beb34245b017e08949a0f2a83c1887eb31f764
tested_at:
tested_fingerprint:
updated: 2026-11-06
---

# Try it: Go SDK client configuration

<!-- runbook-status -->
> **Status:** drafted · 0 of 3 steps · Mode: light · Drafted at 1ac9f123 · 2026-11-06
<!-- /runbook-status -->

## What you're testing

Exercise the public client constructors against test-owned loopback gRPC servers: bound stalled unary calls, retry safe reads, and receive sanitized diagnostics. Run these commands from the SDK checkout. This draft has not been run as an interactive walkthrough.

## Test environment

No persistent fixtures. The Go tests create and stop their own local gRPC servers. The repository's mise toolchain must be installed.

## Steps

### Step 1 of 3: Observe timeout and caller-context behavior (US1)

**Why this step:** A configured timeout should bound a stalled unary call while callers retain control of longer deadlines, polling and streams.

**Needs:** The SDK checkout and its mise toolchain.

**Run:**

```shell
mise exec -- go test ./openshell/v1 -run 'TestClientPolicy_(Timeout|ActiveCancellation|StreamLifetime|PollingLifetime)$' -count=1 -v
```

**What you should see:** All cases pass, including default and explicit deadlines, active cancellation, and streams/polling that outlive the default unary budget.

**If you see something else:** A deadline or stream-lifetime regression fails its named test; toolchain errors indicate missing setup rather than a configuration result.

**Result:**

### Step 2 of 3: Check bounded safe-read retries (US2)

**Why this step:** Transient failures should recover within the configured limits without replaying unsafe operations.

**Needs:** Step 1's toolchain setup.

**Run:**

```shell
mise exec -- go test ./openshell/v1/internal/grpc ./openshell/v1 -run 'TestClientPolicy_(Retry|Backoff)' -count=1 -v
```

**What you should see:** Recovery, exhaustion, method exclusions, shared budgets, cancellation and capped-delay cases pass.

**If you see something else:** Use the failing test name to distinguish an eligibility, attempt-count, timing or cancellation failure.

**Result:**

### Step 3 of 3: Check diagnostics and constructor parity (US3)

**Why this step:** Both constructors should honor the same settings, with useful diagnostics that exclude sensitive data.

**Needs:** Step 1's toolchain setup.

**Run:**

```shell
mise exec -- go test ./openshell/v1/internal/grpc ./openshell/v1 ./openshell/v1/gateway -run 'TestClientPolicy_(Logging|LogSanitization|ConstructorParity)$' -count=1 -v
```

**What you should see:** Logging and synthetic-secret exclusion checks pass, along with direct/gateway timeout, retry and validation parity.

**If you see something else:** A log test failure can identify missing events or leaked data; parity failures identify a setting the gateway wrapper handles differently.

**Result:**

## Coverage

US1: deadlines, cancellation and lifetime boundaries. US2: recovery, bounds and exclusions. US3: diagnostics and sanitization. Constructor parity checks all three settings. This local journey does not cover a live remote gateway deployment.

## Results
