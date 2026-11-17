// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidcclient"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// Discovery, the refresh_token grant and the token file live in
// internal/oidcclient: this package imports gateway, so gateway could not
// use them from here.

var oidcHTTPClient = oidcclient.HTTPClient

type providerConfig = oidcclient.ProviderConfig

func discover(ctx context.Context, issuer string) (*providerConfig, error) {
	return oidcclient.Discover(ctx, issuer)
}

func resetDiscoveryCache() { oidcclient.ResetDiscoveryCache() }

func readToken(dir string) (*oidctoken.Bundle, error) { return oidcclient.ReadToken(dir) }

func writeToken(dir string, b oidctoken.Bundle) error { return oidcclient.WriteToken(dir, b) }

// refreshStoredToken renews an expired stored token with its refresh token
// and rewrites the file (see [oidcclient.Refresh]). It returns a nil token
// and nil error when Login should run the interactive flow instead, and an
// error when Login should stop: the context is done, discovery failed, or
// the new token could not be written. The stored argument only decides
// whether to try; the file read under the lock is what gets refreshed.
//
// In-memory mode does not refresh, since a rotated refresh token that is
// not written back would leave the file holding a revoked one.
func refreshStoredToken(ctx context.Context, cfg *loginConfig, tokenDir string, stored oidctoken.Bundle) (*oauth2.Token, error) {
	if cfg.inMemory || stored.RefreshToken == "" {
		return nil, nil
	}

	// Refresh with the scopes the authorization request uses (openid
	// first) when they were set explicitly or by the gateway's
	// oidc_scopes; with the built-in defaults, omit scope so the provider
	// keeps the original grant.
	var scopes []string
	if cfg.scopesSet {
		scopes = cfg.scopes
	}

	res, err := oidcclient.Refresh(ctx, oidcclient.Request{
		Dir:      tokenDir,
		Issuer:   cfg.issuer,
		ClientID: cfg.clientID,
		Scopes:   scopes,
		Window:   oidcclient.RefreshWindow,
	})
	if err != nil {
		return nil, loginRefreshError(ctx, err)
	}
	return res.Token, nil
}

// loginRefreshError decides what Login does with a failed refresh. It
// returns nil when Login should run the interactive flow: there was
// nothing to refresh, or the refresh failed while ctx was still live.
// Otherwise it returns the error Login reports: the write error, even if
// ctx has also ended; ErrTimeout wrapping ctx's error; or the discovery
// error.
func loginRefreshError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, oidcclient.ErrNotRefreshable):
		return nil
	case errors.Is(err, ErrTokenPersist):
		return err
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
	case errors.Is(err, ErrDiscovery):
		return err
	default:
		return nil
	}
}

// sameClient reports whether a stored bundle was issued for the configured
// issuer (ignoring a trailing slash) and client ID.
func sameClient(b *oidctoken.Bundle, cfg *loginConfig) bool {
	return oidcclient.SameClient(b, cfg.issuer, cfg.clientID)
}
