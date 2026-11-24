# Service discovery

Accessor: `client.ServiceRoutes()` on the concrete SDK client. Consumers can
accept the narrow `ServiceDiscoveryInterface`; existing `ClientInterface` mocks
are unchanged. Discovery reads accepted supervisor installations for one explicit
workspace and requires the same workspace read authorization as the gateway API.

```go
snapshot, err := client.ServiceRoutes().Snapshot(ctx, "default")
if err != nil {
    return err
}
for _, route := range snapshot.Routes {
    fmt.Println(route.Hostname, route.Endpoint, route.ExpiresAt)
}

watch, err := client.ServiceRoutes().Watch(ctx, "default")
if err != nil {
    return err
}
defer watch.Close()
for {
    replacement, err := watch.Recv()
    if errors.Is(err, io.EOF) {
        break // reconnect and relist after normal bounded stream completion
    }
    if err != nil {
        return err // an error is not an empty replacement
    }
    _ = replacement // atomically replace the complete cached workspace view
}
```

The SDK returns complete replacement snapshots without background prefetch or
implicit reconnect. A caller must expire cached entries at `ExpiresAt` even when
discovery is unavailable, reconnect/relist after stream termination, and clear
routes when authorization is revoked. `Close` cancels the RPC and is safe to call
concurrently with one receiving goroutine. Derive a cancelable watch context and
choose snapshot deadlines appropriate for the consumer.

Preserve `Hostname` as TLS SNI and HTTP authority when dialing `Endpoint`.
`InstallationVersion` stays stable across lease-only renewal; `Revision` changes
with expiry and is only an equality token. Installation is neither application
health nor proof of external ingress acceptance. Platform-wide discovery-only
identities and all-workspaces queries are separate work.

Discovery calls the separate `openshell.v1.ServiceDiscovery` service available
in core v0.1.9. `Snapshot` maps to `GetServiceRouteSnapshot`; `Watch` maps to
`WatchServiceRoutes`. Both require bearer authentication with `sandbox:read`
scope and the workspace `user` role. The root `ClientInterface` is unchanged.
