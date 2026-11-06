# Client Policy Research

## Shared Unary Policy

Decision: use a unary interceptor on the existing shared connection. All sub-clients use this connection, so defaults and diagnostics apply consistently. Alternatives rejected: per-sub-client policy copies (drift), whole-operation wrapping (changes lifetime scope), and service-config retries (cannot express the intended logging and conservative safety contract reliably).

The local gRPC v1.83.2 `dialoptions.go:678` documents `WithDisableRetry`: it disables service-config retries while retaining transparent retries when no data reached the wire or the remote application did not process the RPC. This is the intended transport distinction. `NewConnection` will accept variadic dial options; the SDK policy options include retry disablement after any configurable options.

## Verified Safe Read List

Decision: start with this explicit 22-method list, using exact generated `OpenShell_*_FullMethodName` constants. Everything else defaults to no configured retry. Current server source evidence was inspected in the adjacent OpenShell checkout on 2026-11-06; SDK bindings remain pinned to their existing release.

| Methods | Behavior / server source |
|---------|--------------------------|
| `Health`, `GetCurrentUser`, `GetGatewayInfo` | Runtime snapshots and authenticated identity; `grpc/mod.rs`, `auth_rpc.rs` |
| `GetSandbox`, `ListSandboxes`, `GetSandboxTemplate`, `ListSandboxTemplates`, `ListSandboxProviders` | Stored records, redacted provider records; `grpc/sandbox.rs:1033,1065,1217,1248,1396` |
| `GetService`, `ListServices` | Stored endpoint records and response URLs; `grpc/service.rs:240,265` |
| `GetProvider`, `ListProviders`, `GetProviderRefreshStatus` | Redacted records and refresh metadata, no refresh execution; `grpc/provider.rs:2897,2922,4876` |
| `GetGatewayConfig`, `GetSandboxPolicyStatus`, `ListSandboxPolicies`, `GetSandboxLogs`, `GetDraftPolicy`, `GetDraftHistory` | Settings, policy history, buffered logs, selected draft records; `grpc/policy.rs:3780,4900,4964,5401,6005,6840` |
| `GetWorkspace`, `ListWorkspaces`, `ListWorkspaceMembers` | Workspace/membership reads; `grpc/workspace.rs:219,249,615` |

## Exclusions That Require More Than Name Matching

- `GetSandboxProviderStatus` and its peer variant record durable observations. An absent receipt creates a UUID-backed Observe operation; successful paths call `observe_provider_status` (`provider_readiness.rs:348–380,435`). They are excluded.
- `GetProviderCredentials` can call `ensure_fresh` and mint/refresh credentials (`provider_credentials.rs:243–257`), so it is excluded.
- `GetSandboxProviderEnvironment` resolves secret handles through credential backends; excluded conservatively.
- `GetSandboxConfig`, `GetProviderProfile`, `ListProviderProfiles` can invoke externally implemented `SnapshotProviderProfiles` for uncached catalogs (`provider_profile_sources.rs:340–346`). The gateway code is read-only, but extension replay safety is not explicit; exclude in the first version.
- `LintProviderProfiles`, mutations, reports, token operations, session creation, execution, peer notifications and all streams are unclassified/excluded.

## Defaults, Logging and Tests

Decision: default zero waits to 100 ms/5 s, count retries after the initial attempt, reject negative/reversed values and snapshot the caller policy. Backoff is capped exponential without jitter or server hints, per clarified scope. Saturate before duration multiplication; do not reset contexts across attempts.

Decision: Debug for successful logical completions and retries; Error for final failure with a new status-code-only error. Log only method, status, duration, attempts and delay. Do not log bodies, metadata, raw server messages, or original error objects. No streaming lifecycle guarantee is added.

Tests use both controlled invokers and public constructors against a loopback gRPC server. They must distinguish transport status before conversion: `ResourceExhausted` maps to SDK unavailable but remains ineligible. No new dependencies or live gateway fixture are required.
