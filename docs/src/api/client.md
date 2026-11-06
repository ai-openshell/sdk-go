# Client

Constructor: `v1.NewClient(config)`

`Client` is the root entry point for SDK operations. It provides typed accessors
for each resource domain and manages the underlying gRPC connection. The
`ClientInterface` covers the stable core accessors; additive helpers such as
`SandboxTemplates()` are available on the concrete client.

## Methods

| Accessor | Returns | Description |
|----------|---------|-------------|
| `Sandboxes()` | `SandboxInterface` | Sandbox lifecycle management |
| `SandboxTemplates()` | `SandboxTemplateInterface` | Reusable sandbox template management |
| `Providers()` | `ProviderInterface` | Provider CRUD and idempotent ensure |
| `Services()` | `ServiceInterface` | Service exposure and management |
| `Exec()` | `ExecInterface` | Command execution (run, stream, interactive) |
| `Files()` | `FileInterface` | File upload and download |
| `Health()` | `HealthInterface` | Gateway health checking |
| `SSH()` | `SSHInterface` | SSH session and tunnel management |
| `TCP()` | `TCPInterface` | TCP port forwarding |
| `Config()` | `ConfigInterface` | Sandbox and gateway configuration |
| `Policy()` | `PolicyInterface` | Draft policy review workflow |
| `Close()` | `error` | Close the gRPC connection |

Sub-client hierarchy: `Providers()` has two nested accessors:
- `client.Providers().Profiles()` returns `ProfileInterface`
- `client.Providers().Refresh()` returns `RefreshInterface`

## Creating a Client

```go
import v1 "github.com/ai-openshell/sdk-go/openshell/v1"

client, err := v1.NewClient(v1.Config{
    Address: "gateway.example.com:443",
    Auth:    v1.StaticToken("my-token"),
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

## Configuration

The `Config` struct controls connection behavior:

```go
type Config struct {
    Address     string         // Gateway address (host:port)
    TLS         *TLSConfig     // TLS settings (nil uses system defaults)
    Auth        AuthProvider   // Authentication provider
    Timeout     time.Duration  // Default unary deadline without caller deadline
    RetryPolicy *RetryPolicy   // Safe-read retries (nil = no configured retries)
    Logger      Logger         // Unary diagnostics (nil = no configured logging)
}
```

Authentication providers:
- `v1.StaticToken(token)` provides a fixed bearer token
- `v1.NoAuth()` skips authentication (for local development)

## Request deadlines

A positive `Timeout` supplies a deadline to each unary RPC that has no caller
deadline. An explicit caller deadline takes precedence, whether shorter or longer.
All attempts and backoff for one RPC share that deadline; cancellation interrupts
requests and retry waits. Zero supplies no default, and a negative timeout makes
construction fail with an SDK invalid-argument error.

Streams, watches, exec streams (including `Exec.Run`), sessions and tunnels keep
their caller-controlled lifetimes. Unary setup calls still follow the default.
Polling helpers such as `WaitReady` may run longer than `Timeout`; each individual
poll follows the unary rule, and the caller context bounds the overall wait.

## Retries

`RetryPolicy.MaxRetries` counts additional attempts after the first. Nil policy
or zero retries disables configured replay. Zero `InitialWait` defaults to 100 ms;
zero `MaxWait` defaults to 5 s. Delays double from the initial wait, saturate at
the maximum, and have no jitter or server retry hints. Negative fields or a
maximum below the initial wait after defaults fail construction with an SDK
invalid-argument error. Clients copy the policy at construction.

Only original gRPC `Unavailable` responses from these safe unary reads retry:

| Domain | Eligible RPCs |
|--------|---------------|
| Gateway | `Health`, `GetCurrentUser`, `GetGatewayInfo`, `GetGatewayConfig` |
| Sandboxes | `GetSandbox`, `ListSandboxes`, `GetSandboxTemplate`, `ListSandboxTemplates`, `ListSandboxProviders` |
| Services | `GetService`, `ListServices` |
| Providers | `GetProvider`, `ListProviders`, `GetProviderRefreshStatus` |
| Policy | `GetSandboxPolicyStatus`, `ListSandboxPolicies`, `GetSandboxLogs`, `GetDraftPolicy`, `GetDraftHistory` |
| Workspaces | `GetWorkspace`, `ListWorkspaces`, `ListWorkspaceMembers` |

Mutations, execution, session creation, streams and unclassified methods do not
receive configured retries. In particular, provider credential resolution and
`GetSandboxProviderStatus` are excluded. `ResourceExhausted` does not retry, even
though the SDK classifies that error as unavailable. Service-config retries are
disabled so they cannot broaden this list. gRPC may still retry transparently
before the remote application processes a request.

## Diagnostics

A configured `Logger` receives one completion per logical unary RPC with method,
total duration, final status and attempt count. Successful completions and
scheduled retries use Debug; failures use Error with a status-only error. Retry
fields include the upcoming attempt number and delay. The logger must support
concurrent calls and works without a retry policy.

Diagnostics exclude bodies, authentication metadata, credentials and raw server
error text. Nil logger disables this facility. Stream lifecycle diagnostics and
global gRPC logging are outside this contract.

## Testing

For unit tests, use the fake client instead of a real connection:

```go
import "github.com/ai-openshell/sdk-go/openshell/v1/fake"

client := fake.NewClient()
defer client.Close()
```

The fake client implements the full `ClientInterface` with in-memory stores.
See [Testing](../testing.md) for details.

See also: [Getting Started](../getting-started.md), [Architecture](../architecture.md)
