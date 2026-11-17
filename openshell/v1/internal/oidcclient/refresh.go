// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

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

// RefreshWindow is how long before expiry a token is due for refresh: the
// expiry margin of oauth2.Token.Valid and the default leeway of
// v1.RefreshableToken.
const RefreshWindow = 10 * time.Second

// now is the clock used to decide whether a token is fresh. Tests replace
// it.
var now = time.Now

// refreshLocks serializes refreshes of one token file within the process,
// so concurrent callers do not spend the same refresh token twice. It maps
// each absolute token directory to a one-slot semaphore and keeps one
// entry per directory ever refreshed, which is a handful of gateways in
// practice.
var refreshLocks sync.Map

// LockDir waits for the refresh lock of dir until ctx ends, and returns
// the function that releases it. When ctx ends first, it returns ctx's
// error.
func LockDir(ctx context.Context, dir string) (func(), error) {
	key := filepath.Clean(dir)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	slot, _ := refreshLocks.LoadOrStore(key, make(chan struct{}, 1))
	sem := slot.(chan struct{})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Fresh reports whether tok has an access token that does not expire
// within window. A token without an expiry is fresh. With a 10 second
// window it is the same test as oauth2.Token.Valid.
func Fresh(tok *oauth2.Token, window time.Duration) bool {
	if tok == nil || tok.AccessToken == "" {
		return false
	}
	return tok.Expiry.IsZero() || !tok.Expiry.Round(0).Add(-window).Before(now())
}

// Request describes a refresh of the token stored in a directory.
type Request struct {
	// Dir is the directory that holds oidc_token.json.
	Dir string

	// Issuer and ClientID identify the provider and client the stored
	// token must belong to. A file written for another issuer or client is
	// not refreshed.
	Issuer   string
	ClientID string

	// Scopes are sent as scope when not empty.
	Scopes []string

	// Window is how close to expiry the stored token may be and still be
	// returned without a refresh.
	Window time.Duration
}

// Refresh renews the token stored in req.Dir with its refresh token and
// rewrites the file.
//
// The file is shared with the CLI, and providers that rotate refresh
// tokens revoke the old one on use. Under a per-directory lock the file is
// read again first, so a token another process or goroutine has just
// refreshed is returned as is. When the refresh is rejected with
// invalid_grant, the file is read once more: a fresh token another process
// wrote for the same issuer and client is returned as is, and an expired
// one with a different refresh token is refreshed once with that token.
// Like the Python SDK, it takes no file lock across processes. If the
// response has no refresh token, the stored one is kept.
//
// ctx bounds the lock wait, discovery and the token request. The errors
// are ctx's error while waiting for the lock, ErrNotRefreshable when there
// is nothing to refresh, ErrDiscovery, ErrRefresh, and ErrTokenPersist when
// the new token cannot be written. In the last case the file keeps the old
// refresh token, as with the CLI.
func Refresh(ctx context.Context, req Request) (*oauth2.Token, error) {
	unlock, err := LockDir(ctx, req.Dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	current, err := ReadToken(req.Dir)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrNotRefreshable, err)
	case current == nil:
		return nil, fmt.Errorf("%w: %s does not exist", ErrNotRefreshable, oidctoken.FileName)
	case !sameClient(current, req):
		return nil, fmt.Errorf("%w: it was issued for another issuer or client", ErrNotRefreshable)
	}
	if tok := current.Token(); Fresh(tok, req.Window) {
		return tok, nil
	}
	if current.RefreshToken == "" {
		return nil, fmt.Errorf("%w: it has no refresh token", ErrNotRefreshable)
	}
	stored := *current

	provider, err := Discover(ctx, req.Issuer)
	if err != nil {
		return nil, err
	}

	tok, err := refreshToken(ctx, provider.TokenEndpoint, req.ClientID, stored.RefreshToken, req.Scopes)
	if errors.Is(err, ErrInvalidGrant) {
		peer, readErr := ReadToken(req.Dir)
		if readErr != nil || peer == nil || !sameClient(peer, req) {
			return nil, fmt.Errorf("%w: %w", ErrNotRefreshable, err)
		}
		if peerTok := peer.Token(); Fresh(peerTok, req.Window) {
			return peerTok, nil
		}
		if peer.RefreshToken == "" || peer.RefreshToken == stored.RefreshToken {
			return nil, fmt.Errorf("%w: %w", ErrNotRefreshable, err)
		}
		stored = *peer
		tok, err = refreshToken(ctx, provider.TokenEndpoint, req.ClientID, stored.RefreshToken, req.Scopes)
	}
	if err != nil {
		return nil, err
	}

	// Providers that do not rotate refresh tokens omit one from the
	// response; keep the stored token so the next refresh still works.
	if tok.RefreshToken == "" {
		tok.RefreshToken = stored.RefreshToken
	}

	b := oidctoken.FromToken(tok, stored.Issuer, stored.ClientID)
	b.ClientCredentials = stored.ClientCredentials
	if err := WriteToken(req.Dir, b); err != nil {
		return nil, err
	}
	return tok, nil
}

// sameClient reports whether a stored bundle was issued for the requested
// issuer (ignoring a trailing slash) and client ID.
func sameClient(b *oidctoken.Bundle, req Request) bool {
	return NormalizeIssuer(b.Issuer) == NormalizeIssuer(req.Issuer) && b.ClientID == req.ClientID
}

// grantResponse is the token endpoint's JSON response.
type grantResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
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
		return nil, fmt.Errorf("%w: failed to create token request: %v", ErrRefresh, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token request failed: %v", ErrRefresh, err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read token response: %v", ErrRefresh, err)
	}

	var tokResp grantResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("%w: invalid token response JSON: %v", ErrRefresh, err)
	}

	if tokResp.Error == "invalid_grant" {
		return nil, fmt.Errorf("%w: %w", ErrRefresh, ErrInvalidGrant)
	}
	if tokResp.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrRefresh, tokResp.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrRefresh, resp.StatusCode)
	}
	if tokResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: token response has no access_token", ErrRefresh)
	}

	// An absent expires_in leaves the token without an expiry, as the code
	// exchange does. A present expires_in must be positive; zero or a
	// negative value is rejected as malformed.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	if _, present := fields["expires_in"]; present && tokResp.ExpiresIn <= 0 {
		return nil, fmt.Errorf("%w: token response requires a positive expires_in", ErrRefresh)
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
