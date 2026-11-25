// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

var (
	// errTokenRefresh is returned when a refresh_token grant fails. Login
	// does not surface it: a failed refresh falls back to the interactive
	// flow.
	errTokenRefresh = errors.New("oidc: token refresh failed")

	// errInvalidGrant marks a refresh rejected with invalid_grant. The
	// refresh token has expired or been revoked, or another process has
	// already used it.
	errInvalidGrant = errors.New("oidc: invalid_grant")
)

// refreshLocks serializes refreshes of one token file within the process,
// so concurrent Login calls do not spend the same refresh token twice. It
// maps each absolute token directory to a one-slot semaphore and keeps one
// entry per directory ever refreshed, which is a handful of gateways in
// practice.
var refreshLocks sync.Map

// lockTokenDir waits for the refresh lock of dir until ctx ends, and
// returns the function that releases it.
func lockTokenDir(ctx context.Context, dir string) (func(), error) {
	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	slot, _ := refreshLocks.LoadOrStore(key, make(chan struct{}, 1))
	sem := slot.(chan struct{})
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
	}
}

// refreshStoredToken renews an expired stored token with its refresh token
// and rewrites the file. It returns a nil token and nil error when Login
// should run the interactive flow instead, and an error when Login should
// stop: the context is done, discovery failed, or the new token could not
// be written.
//
// The file is shared with the CLI, and providers that rotate refresh
// tokens revoke the old one on use. Under a per-directory lock the file is
// read again first, so a token another process or goroutine has just
// refreshed is used as is. When the refresh is rejected with invalid_grant,
// the file is read once more: a valid token another process wrote for the
// same issuer and client is used as is, and an expired one with a
// different refresh token is refreshed once with that token. Like the
// Python SDK, it takes no file lock across processes. The stored argument
// only decides whether to try; the file read under the lock is what gets
// refreshed.
//
// In-memory mode does not refresh, since a rotated refresh token that is
// not written back would leave the file holding a revoked one. If the
// write fails after a rotation, the error is returned and the file keeps
// the old refresh token, as with the CLI.
func refreshStoredToken(ctx context.Context, cfg *loginConfig, tokenDir string, stored oidctoken.Bundle) (*oauth2.Token, error) {
	if cfg.inMemory || stored.RefreshToken == "" {
		return nil, nil
	}

	unlock, err := lockTokenDir(ctx, tokenDir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	current, err := readToken(tokenDir)
	if err != nil || current == nil || !sameClient(current, cfg) {
		return nil, nil
	}
	if tok := current.Token(); tok.Valid() {
		return tok, nil
	}
	if current.RefreshToken == "" {
		return nil, nil
	}
	stored = *current

	provider, err := discover(ctx, cfg.issuer)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrTimeout, ctxErr)
		}
		return nil, err
	}

	// Refresh with the scopes the authorization request uses (openid
	// first) when they were set explicitly or by the gateway's
	// oidc_scopes; with the built-in defaults, omit scope so the provider
	// keeps the original grant.
	var scopes []string
	if cfg.scopesSet {
		scopes = cfg.scopes
	}

	tok, err := refreshToken(ctx, provider.TokenEndpoint, cfg.clientID, stored.RefreshToken, scopes)
	if errors.Is(err, errInvalidGrant) {
		peer, readErr := readToken(tokenDir)
		if readErr != nil || peer == nil || !sameClient(peer, cfg) {
			return nil, nil
		}
		if peerTok := peer.Token(); peerTok.Valid() {
			return peerTok, nil
		}
		if peer.RefreshToken == "" || peer.RefreshToken == stored.RefreshToken {
			return nil, nil
		}
		stored = *peer
		tok, err = refreshToken(ctx, provider.TokenEndpoint, cfg.clientID, stored.RefreshToken, scopes)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrTimeout, ctxErr)
		}
		return nil, nil
	}

	// Providers that do not rotate refresh tokens omit one from the
	// response; keep the stored token so the next refresh still works.
	if tok.RefreshToken == "" {
		tok.RefreshToken = stored.RefreshToken
	}

	b := oidctoken.FromToken(tok, stored.Issuer, stored.ClientID)
	b.ClientCredentials = stored.ClientCredentials
	if err := writeToken(tokenDir, b); err != nil {
		return nil, err
	}
	return tok, nil
}

// sameClient reports whether a stored bundle was issued for the configured
// issuer (ignoring a trailing slash) and client ID.
func sameClient(b *oidctoken.Bundle, cfg *loginConfig) bool {
	return normalizeIssuer(b.Issuer) == normalizeIssuer(cfg.issuer) && b.ClientID == cfg.clientID
}

// refreshToken exchanges a refresh token for new tokens at the token
// endpoint (RFC 6749 Section 6). Like the CLI, it sends grant_type,
// refresh_token and client_id as a public client, plus scope when scopes
// are given.
//
// The refresh token is never included in error messages.
func refreshToken(ctx context.Context, tokenEndpoint, clientID, refresh string, scopes []string) (*oauth2.Token, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {clientID},
	}
	if len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create token request: %v", errTokenRefresh, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token request failed: %v", errTokenRefresh, err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read token response: %v", errTokenRefresh, err)
	}

	var tokResp tokenResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("%w: invalid token response JSON: %v", errTokenRefresh, err)
	}

	if tokResp.Error == "invalid_grant" {
		return nil, fmt.Errorf("%w: %w", errTokenRefresh, errInvalidGrant)
	}
	if tokResp.Error != "" {
		return nil, fmt.Errorf("%w: %s", errTokenRefresh, tokResp.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", errTokenRefresh, resp.StatusCode)
	}
	if tokResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: token response has no access_token", errTokenRefresh)
	}

	// An absent expires_in leaves the token without an expiry, as the code
	// exchange does. A present expires_in must be positive; zero or a
	// negative value is rejected as malformed.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	if _, present := fields["expires_in"]; present && tokResp.ExpiresIn <= 0 {
		return nil, fmt.Errorf("%w: token response requires a positive expires_in", errTokenRefresh)
	}

	tok := &oauth2.Token{
		AccessToken:  tokResp.AccessToken,
		RefreshToken: tokResp.RefreshToken,
		TokenType:    tokResp.TokenType,
	}
	if tokResp.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	}
	return tok, nil
}
