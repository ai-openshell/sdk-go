# Gateway

Package: `openshell/v1/gateway`

The gateway package reads on-disk gateway configurations created by the
OpenShell Rust CLI and constructs fully wired SDK clients. It eliminates
the boilerplate of locating config files, parsing metadata, loading
tokens, and wiring auth providers.

## Quick Start

```go
import "github.com/ai-openshell/sdk-go/openshell/v1/gateway"

// Connect to a named gateway
client, err := gateway.NewClient("prod")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

## Functions

| Function | Description |
|----------|-------------|
| `NewClient(name, opts...)` | Create a fully wired SDK client from gateway config |
| `LoadConfig(name)` | Parse gateway config without connecting |
| `ListGateways()` | Enumerate all available gateways |

### NewClient

```go
func NewClient(name string, opts ...ClientOption) (*v1.Client, error)
```

Creates a fully configured SDK client for the named gateway. If `name`
is empty, the active gateway (set via `openshell gateway use`) is used.

The function resolves the gateway directory, parses `metadata.json`,
loads tokens lazily, maps the auth mode to an SDK auth provider, and
applies any `ClientOption` values before delegating to `v1.NewClient`.

```go
// Named gateway
client, err := gateway.NewClient("prod")

// Active gateway
client, err := gateway.NewClient("")

// With options
client, err := gateway.NewClient("staging",
    gateway.WithTimeout(10 * time.Second),
    gateway.WithLogger(myLogger),
)
```

**Errors**: `ErrGatewayNotFound`, `ErrConfigParse`,
`ErrUnsupportedAuthMode`, `ErrInvalidGatewayName`, `ErrNoActiveGateway`

### LoadConfig

```go
func LoadConfig(name string) (*Config, error)
```

Parses gateway configuration without creating a client connection.
Returns a frozen snapshot; changes to on-disk files after the call
are not reflected.

```go
cfg, err := gateway.LoadConfig("staging")
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Endpoint: %s, Auth: %s\n", cfg.Endpoint, cfg.AuthMode)
```

### ListGateways

```go
func ListGateways() ([]Info, error)
```

Enumerates all available gateways from user and system directories.
User gateways appear first. Duplicate names resolve to user precedence.
Returns an empty slice (not an error) when no gateways are configured.

```go
gateways, err := gateway.ListGateways()
for _, gw := range gateways {
    fmt.Printf("%s (active=%v, source=%s)\n", gw.Name, gw.Active, gw.Source)
}
```

## Types

### Config

```go
type Config struct {
    Name     string       // Validated gateway name
    Endpoint string       // Host:port of the gateway
    AuthMode AuthMode     // Resolved auth mode
    Source   ConfigSource // User or System origin
    Dir      string       // Absolute path to gateway config directory
}
```

### Info

```go
type Info struct {
    Name   string       // Gateway name from directory listing
    Active bool         // Whether this is the active gateway
    Source ConfigSource // User or System origin
}
```

### AuthMode

| Value | Constant | SDK AuthProvider |
|-------|----------|-----------------|
| `""` or `"none"` | `AuthModeNone` | `v1.NoAuth()` |
| `"plaintext"` | `AuthModePlaintext` | `v1.NoAuth()` + insecure TLS |
| `"cloudflare_jwt"` | `AuthModeCloudflareJWT` | Lazy edge token auth |
| `"oidc"` | `AuthModeOIDC` | `v1.RefreshableToken` that reads `oidc_token.json` near expiry and renews the token with its refresh token |
| `"mtls"` | `AuthModeMTLS` | Unsupported (use `WithAuth`) |

With `AuthModeCloudflareJWT`, the SDK rereads the edge token file on each request, so a token refreshed by `openshell gateway login` takes effect without a restart. Keep the token file readable.

With `AuthModeOIDC`, the SDK renews the token in `oidc_token.json` with its refresh token when the token is within 30 seconds of expiry, as the CLI does, and rewrites the file in the CLI's format (owner-only on Unix), so the CLI and other SDKs pick up a rotated refresh token. For tokens valid for less than a minute, renewal starts at half the token's lifetime once the client has renewed one. Renewal needs `oidc_issuer` and `oidc_client_id` in `metadata.json` and a token issued for them; tokens without a refresh token or an expiry are not renewed. The SDK sends `oidc_scopes` with `openid` first, or no scope when `oidc_scopes` is not set.

RPCs that find the token due while it has not expired wait for the renewal until 2 seconds after it started, then send the current token and let the renewal finish in the background. An RPC with a deadline waits at most half the time it has left and then sends the token the client already holds; if the client holds none yet, the RPC fails with DeadlineExceeded when its deadline ends first. If renewal fails while the token is still usable, the source returns that token to the client and waits at least 5 seconds before trying again. Returning a usable token resets the client's backoff.

An RPC that finds the token expired, or sees it expire while waiting, waits for the renewal, at most 30 seconds for the lock and discovery and then for each token request. If the renewal fails, the RPC fails as described in [Error Handling](#error-handling), with the reason in the message, such as the provider's error code or HTTP status, and later RPCs retry with the client's backoff of 1 second, doubling up to 30 seconds; in between, they fail at once with the last error. Every failed renewal also goes to the logger set with `gateway.WithLogger`.

If a renewed token cannot be written, for example on a full disk, the client keeps it in memory and uses it, since a provider that rotates refresh tokens has already revoked the one in the file, and the write error goes to the logger. The next renewal writes it first. While that write fails and the refresh token was rotated, renewals send no token request, because a provider may end the session when it sees a revoked refresh token; the client then works until the token expires. A login or a token written by another process replaces it.

A renewal is not tied to the client: closing the client does not stop it, and a renewal still running when the process exits is cut off. With a provider that rotates refresh tokens, the stored refresh token may then no longer work. A process killed while it writes the file can leave a `.oidc-token-*` temporary file in the gateway directory.

The SDK does not lock `oidc_token.json` against other processes, as the CLI and the Python SDK do not. With a provider that rotates refresh tokens, two processes that renew at the same moment spend the same refresh token, and one gets `invalid_grant`. It uses the token the other wrote if that is already in the file, and otherwise fails that renewal and picks the token up on a later one. A provider that revokes all tokens of a session on refresh token reuse ends the session instead.

The SDK tries `edge_token` first and falls back to the legacy `cf_token` only when `edge_token` is absent. Logout takes effect for a running client once neither token file remains. Removing only `edge_token` still permits authentication if a legacy `cf_token` remains.

### ClientOption

| Constructor | Effect |
|-------------|--------|
| `WithLogger(l)` | Set logger on the SDK client |
| `WithTimeout(d)` | Set connection timeout |
| `WithTLS(cfg)` | Override TLS settings from gateway config |
| `WithAuth(provider)` | Override auto-resolved auth provider |
| `WithRetryPolicy(p)` | Set retry policy |

## Error Handling

All errors support `errors.Is` for classification:

```go
client, err := gateway.NewClient("my-gateway")
if errors.Is(err, gateway.ErrGatewayNotFound) {
    fmt.Println("Gateway not configured. Run: openshell gateway add my-gateway")
}
```

Tokens load lazily, so `NewClient` does not return `ErrTokenLoad`. For `oidc`
gateways, a missing, malformed, or expired `oidc_token.json`, or a failed renewal
of an expired token, surfaces on RPCs as
an error for which `v1.IsUnauthenticated(err)` is true, and its message includes
``run `openshell gateway login <name>` ``. An RPC whose context ends while it
waits for the renewal fails with DeadlineExceeded or Canceled instead.

| Error | Meaning |
|-------|---------|
| `ErrGatewayNotFound` | No gateway directory in user or system paths |
| `ErrConfigParse` | metadata.json missing or malformed |
| `ErrTokenLoad` | Token file missing, unreadable, malformed, or expired, or an expired OIDC token could not be renewed; RPCs report it as Unauthenticated |
| `ErrUnsupportedAuthMode` | Unrecognized auth_mode value |
| `ErrInvalidGatewayName` | Name fails validation |
| `ErrNoActiveGateway` | No active gateway configured |

## On-Disk Layout

The package reads gateway metadata from these locations:

```
$XDG_CONFIG_HOME/openshell/          (user, default: ~/.config/openshell/)
├── active_gateway                   # Plain text: active gateway name
└── gateways/
    └── <name>/
        ├── metadata.json            # {"endpoint":"...","auth_mode":"...","name":"..."}
        ├── edge_token               # Cloudflare edge JWT (plaintext)
        ├── cf_token                 # Legacy edge token (fallback)
        └── oidc_token.json          # OIDC token bundle

/etc/openshell/gateways/             (system, fallback)
```

User gateways take precedence over system gateways with the same name.

## Thread Safety

All exported functions (`NewClient`, `LoadConfig`, `ListGateways`) are
safe for concurrent use from multiple goroutines. Token loading uses
internal synchronization.
