// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package oidcclient holds the OIDC code that both the oidc and gateway
// packages need: discovery, the refresh_token grant, and reading and
// writing oidc_token.json. The oidc package imports gateway, so this code
// cannot live in oidc if gateway is to use it.
package oidcclient

import "errors"

var (
	// ErrDiscovery is returned when the OIDC discovery document cannot be
	// fetched or parsed. The oidc package exports it as oidc.ErrDiscovery.
	ErrDiscovery = errors.New("oidc: discovery failed")

	// ErrTokenPersist is returned when oidc_token.json cannot be read or
	// written. The oidc package exports it as oidc.ErrTokenPersist.
	ErrTokenPersist = errors.New("oidc: token persistence failed")

	// ErrRefresh is returned when a refresh_token grant fails.
	ErrRefresh = errors.New("oidc: token refresh failed")

	// ErrInvalidGrant marks a refresh rejected with invalid_grant. The
	// refresh token has expired or been revoked, or another process has
	// already used it.
	ErrInvalidGrant = errors.New("oidc: invalid_grant")

	// ErrNotRefreshable is returned when the stored token cannot be
	// refreshed: the file is missing or unreadable, was issued for another
	// issuer or client, has no refresh token, or holds a refresh token the
	// provider rejected with no newer one to retry with.
	ErrNotRefreshable = errors.New("oidc: stored token cannot be refreshed")
)
