// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package gateway reads on-disk gateway configurations created by the
// OpenShell Rust CLI and constructs fully wired SDK clients.
//
// The package resolves XDG config paths, validates gateway names, loads
// tokens lazily, maps auth modes to existing auth providers, and provides
// one-call convenience constructors. This eliminates 20+ lines of
// boilerplate for Go programs connecting to gateways managed by the CLI.
//
// # Quick Start
//
// Connect to a named gateway:
//
//	client, err := gateway.NewClient("prod")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
// Connect to the active gateway (set via `openshell gateway use`):
//
//	client, err := gateway.NewClient("")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
// Inspect configuration without creating a client:
//
//	cfg, err := gateway.LoadConfig("staging")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Endpoint: %s, Auth: %s\n", cfg.Endpoint, cfg.AuthMode)
//
// List all configured gateways:
//
//	gateways, err := gateway.ListGateways()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, gw := range gateways {
//	    fmt.Printf("%s (active=%v, source=%s)\n", gw.Name, gw.Active, gw.Source)
//	}
//
// # On-Disk Layout
//
// The package reads gateway metadata from the following locations:
//
//	$XDG_CONFIG_HOME/openshell/gateways/<name>/metadata.json   (user)
//	/etc/openshell/gateways/<name>/metadata.json                (system)
//
// Token files (edge_token, cf_token, oidc_token.json) sit alongside
// metadata.json and are read on each authentication attempt, so tokens
// refreshed by the CLI are picked up. When oidc_token.json holds a refresh
// token and was issued for the OIDC issuer and client ID that metadata.json
// names, the client also renews the token itself within 30 seconds of
// expiry, or at half the token's lifetime for tokens valid for less than a
// minute once it has renewed one, and rewrites the file in the CLI's
// format. A missing or empty file, or a failed renewal of an expired
// token, fails the RPC that needs it as Unauthenticated with the
// [ErrTokenLoad] message; an RPC whose deadline ends while it waits for the
// renewal fails with DeadlineExceeded or Canceled instead.
//
// # Error Handling
//
// The package provides typed errors for precise failure classification:
//
//   - [ErrGatewayNotFound]: no gateway directory found
//   - [ErrConfigParse]: metadata.json missing or malformed
//   - [ErrTokenLoad]: token file missing, unreadable, malformed, or expired, or an expired OIDC token could not be renewed; RPCs report it as Unauthenticated
//   - [ErrUnsupportedAuthMode]: unrecognized auth_mode value
//   - [ErrInvalidGatewayName]: name fails validation
//   - [ErrNoActiveGateway]: no active gateway configured
//
// All errors support [errors.Is] for classification.
//
// # Thread Safety
//
// All exported functions are safe for concurrent use from multiple
// goroutines.
package gateway
