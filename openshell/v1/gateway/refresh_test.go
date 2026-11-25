// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidcclient"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// tokenProvider is a fake OIDC provider. Its token endpoint records each
// request form and answers through respond.
type tokenProvider struct {
	*httptest.Server
	mu      sync.Mutex
	forms   []url.Values
	respond func(form url.Values) (int, string)
}

func newTokenProvider(t *testing.T, respond func(form url.Values) (int, string)) *tokenProvider {
	t.Helper()
	oidcclient.ResetDiscoveryCache()
	p := &tokenProvider{respond: respond}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 p.URL,
			"authorization_endpoint": p.URL + "/authorize",
			"token_endpoint":         p.URL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token request: %v", err)
		}
		p.mu.Lock()
		p.forms = append(p.forms, r.PostForm)
		p.mu.Unlock()
		status, body := p.respond(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// requests returns the forms of all token requests so far.
func (p *tokenProvider) requests() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.forms...)
}

// rotating answers every refresh with a new access token valid for an hour
// and a rotated refresh token.
func rotating(url.Values) (int, string) {
	return http.StatusOK, `{"access_token":"refreshed-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`
}

// unavailable answers every refresh with HTTP 503.
func unavailable(url.Values) (int, string) {
	return http.StatusServiceUnavailable, `{}`
}

// bundleFor returns a token file for issuer and client "openshell-cli"
// with the given refresh token and expiry relative to now.
func bundleFor(issuer, access, refresh string, expiresIn time.Duration) oidctoken.Bundle {
	return oidctoken.FromToken(&oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		Expiry:       time.Now().Add(expiresIn),
	}, issuer, "openshell-cli")
}

// storeToken writes oidc_token.json for issuer and client "openshell-cli".
func storeToken(t *testing.T, dir, issuer, access, refresh string, expiresIn time.Duration) {
	t.Helper()
	require.NoError(t, oidcclient.WriteToken(dir, bundleFor(issuer, access, refresh, expiresIn)))
}

// storeTokenFromHandler is storeToken for a provider's handler goroutine,
// where the test must not be stopped.
func storeTokenFromHandler(t *testing.T, dir, issuer, access, refresh string, expiresIn time.Duration) {
	if err := oidcclient.WriteToken(dir, bundleFor(issuer, access, refresh, expiresIn)); err != nil {
		t.Errorf("store token: %v", err)
	}
}

func readBundle(t *testing.T, dir string) oidctoken.Bundle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, oidctoken.FileName))
	require.NoError(t, err)
	var b oidctoken.Bundle
	require.NoError(t, json.Unmarshal(data, &b))
	return b
}

// oidcConfig is an OIDC gateway "prod" in dir whose metadata names issuer,
// client "openshell-cli" and scopes.
func oidcConfig(dir, issuer, scopes string) *Config {
	return &Config{
		AuthMode: AuthModeOIDC, Dir: dir, Name: "prod",
		OIDCIssuer: issuer, OIDCClientID: "openshell-cli", OIDCScopes: scopes,
	}
}

// oidcSource returns the token source NewClient builds for oidcConfig. The
// test waits for its background refreshes before its directories go away.
func oidcSource(t *testing.T, dir, issuer string, logger types.Logger) *diskTokenSource {
	t.Helper()
	src := newOIDCTokenSource(oidcConfig(dir, issuer, ""), logger)
	t.Cleanup(src.background.Wait)
	return src
}

// oidcAuth returns the auth provider NewClient builds for oidcConfig. Use
// it only with tokens that do not start a background refresh: expired, or
// outside the window.
func oidcAuth(t *testing.T, dir, issuer, scopes string) types.AuthProvider {
	t.Helper()
	auth, err := resolveAuthProvider(oidcConfig(dir, issuer, scopes), nil)
	require.NoError(t, err)
	return auth
}

// setRefreshWait shortens refreshWait for the test.
func setRefreshWait(t *testing.T, d time.Duration) {
	old := refreshWait
	refreshWait = d
	t.Cleanup(func() { refreshWait = old })
}

// setRefreshRetryDelay changes refreshRetryDelay for the test.
func setRefreshRetryDelay(t *testing.T, d time.Duration) {
	old := refreshRetryDelay
	refreshRetryDelay = d
	t.Cleanup(func() { refreshRetryDelay = old })
}

// setRefreshWaitTimeout shortens refreshWaitTimeout for the test.
func setRefreshWaitTimeout(t *testing.T, d time.Duration) {
	old := refreshWaitTimeout
	refreshWaitTimeout = d
	t.Cleanup(func() { refreshWaitTimeout = old })
}

// recordingLogger records the errors it is given.
type recordingLogger struct {
	mu   sync.Mutex
	errs []error
	msgs []string
	kvs  []any
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Error(err error, msg string, keysAndValues ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, err)
	l.msgs = append(l.msgs, msg)
	l.kvs = keysAndValues
}

func (l *recordingLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

func (l *recordingLogger) lastKeysAndValues() []any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.kvs
}

func (l *recordingLogger) errors() []error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]error(nil), l.errs...)
}

// tokenWithin calls src.Token and fails the test if it takes longer than
// limit, which shows that Token did not wait for the provider or the lock.
func tokenWithin(t *testing.T, src *diskTokenSource, limit time.Duration) (*oauth2.Token, error) {
	t.Helper()
	type result struct {
		tok *oauth2.Token
		err error
	}
	done := make(chan result, 1)
	go func() {
		tok, err := src.Token()
		done <- result{tok, err}
	}()
	select {
	case res := <-done:
		return res.tok, res.err
	case <-time.After(limit):
		t.Fatalf("Token took longer than %v", limit)
		return nil, nil
	}
}

func TestOIDCAuth_RefreshesExpiredToken(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	md, err := oidcAuth(t, dir, p.URL, "").GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer refreshed-access", md["authorization"])

	reqs := p.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "refresh_token", reqs[0].Get("grant_type"))
	assert.Equal(t, "stored-refresh", reqs[0].Get("refresh_token"))
	assert.Equal(t, "openshell-cli", reqs[0].Get("client_id"))
	assert.False(t, reqs[0].Has("scope"), "no scope without oidc_scopes")

	b := readBundle(t, dir)
	assert.Equal(t, "refreshed-access", b.AccessToken)
	assert.Equal(t, "rotated-refresh", b.RefreshToken)
	assert.Equal(t, p.URL, b.Issuer)
	assert.Equal(t, "openshell-cli", b.ClientID)
	require.NotNil(t, b.ExpiresAt)
	assert.InDelta(t, time.Now().Add(time.Hour).Unix(), int64(*b.ExpiresAt), 5)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, oidctoken.FileName))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

// TestDiskTokenSource_RenewsBeforeExpiry verifies that a token inside the
// 30 s window is renewed before Token returns when the provider answers
// within refreshWait.
func TestDiskTokenSource_RenewsBeforeExpiry(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expiring-access", "stored-refresh", 20*time.Second)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	assert.Equal(t, "rotated-refresh", readBundle(t, dir).RefreshToken)
	assert.Len(t, p.requests(), 1)
}

// TestDiskTokenSource_SlowRenewalFinishesInBackground verifies that when
// the provider takes longer than refreshWait, Token returns the current
// token and the renewal still finishes and is used by the next call.
func TestDiskTokenSource_SlowRenewalFinishesInBackground(t *testing.T) {
	setRefreshWait(t, 100*time.Millisecond)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expiring-access", "stored-refresh", 20*time.Second)
	src := oidcSource(t, dir, p.URL, nil)

	tok, err := tokenWithin(t, src, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "expiring-access", tok.AccessToken)

	answer()
	src.background.Wait()
	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	assert.Len(t, p.requests(), 1)
}

func TestOIDCAuth_DoesNotRefreshOutsideWindow(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	// Well outside the window: the file keeps whole seconds, so a margin
	// of a second could fall inside it.
	storeToken(t, dir, p.URL, "valid-access", "stored-refresh", refreshWindow+time.Minute)

	md, err := oidcAuth(t, dir, p.URL, "").GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer valid-access", md["authorization"])
	assert.Empty(t, p.requests())
}

func TestDiskTokenSource_NoRefreshWithoutRefreshTokenOrExpiry(t *testing.T) {
	p := newTokenProvider(t, rotating)

	t.Run("expired, no refresh token", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "", -time.Minute)
		_, err := oidcSource(t, dir, p.URL, nil).Token()
		require.ErrorIs(t, err, ErrTokenLoad)
		assert.Contains(t, err.Error(), "expired at")
		assert.Contains(t, err.Error(), loginHint)
	})
	t.Run("expiring, no refresh token", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expiring-access", "", 5*time.Second)
		tok, err := oidcSource(t, dir, p.URL, nil).Token()
		require.NoError(t, err)
		assert.Equal(t, "expiring-access", tok.AccessToken)
	})
	t.Run("no expiry", func(t *testing.T) {
		dir := t.TempDir()
		b := oidctoken.Bundle{AccessToken: "forever", RefreshToken: "stored-refresh", Issuer: p.URL, ClientID: "openshell-cli"}
		require.NoError(t, oidcclient.WriteToken(dir, b))
		tok, err := oidcSource(t, dir, p.URL, nil).Token()
		require.NoError(t, err)
		assert.Equal(t, "forever", tok.AccessToken)
	})
	assert.Empty(t, p.requests())
}

// TestDiskTokenSource_NoRefreshWithoutMetadataClient verifies that a
// gateway whose metadata.json names no issuer or client ID does not
// refresh, as Login refuses to: the refresh token would go to whatever
// issuer the token file names.
func TestDiskTokenSource_NoRefreshWithoutMetadataClient(t *testing.T) {
	p := newTokenProvider(t, rotating)
	for name, cfg := range map[string]func(dir string) *Config{
		"no issuer": func(dir string) *Config {
			c := oidcConfig(dir, p.URL, "")
			c.OIDCIssuer = ""
			return c
		},
		"no client ID": func(dir string) *Config {
			c := oidcConfig(dir, p.URL, "")
			c.OIDCClientID = ""
			return c
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
			src := newOIDCTokenSource(cfg(dir), nil)
			t.Cleanup(src.background.Wait)

			_, err := src.Token()
			require.ErrorIs(t, err, ErrTokenLoad)
			assert.Contains(t, err.Error(), "expired at")

			storeToken(t, dir, p.URL, "expiring-access", "stored-refresh", 5*time.Second)
			tok, err := src.Token()
			require.NoError(t, err)
			assert.Equal(t, "expiring-access", tok.AccessToken)
		})
	}
	assert.Empty(t, p.requests())
}

func TestOIDCAuth_RefreshSendsGatewayScopes(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	_, err := oidcAuth(t, dir, p.URL, "sandbox:read openid offline_access").GetRequestMetadata(context.Background())
	require.NoError(t, err)
	reqs := p.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "openid sandbox:read offline_access", reqs[0].Get("scope"))
}

func TestDiskTokenSource_RefreshKeepsRefreshTokenWhenNotRotated(t *testing.T) {
	p := newTokenProvider(t, func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"refreshed-access","token_type":"Bearer","expires_in":3600}`
	})
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	assert.Equal(t, "stored-refresh", readBundle(t, dir).RefreshToken)
}

func TestDiskTokenSource_RefreshFailureReportsReason(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
		wantIs error
	}{
		"invalid_grant": {
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant","error_description":"Token is not active"}`,
			want:   "invalid_grant",
			wantIs: oidcclient.ErrInvalidGrant,
		},
		"provider error code": {
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_client","error_description":"stored-refresh leaked?"}`,
			want:   `"invalid_client"`,
			wantIs: oidcclient.ErrRefresh,
		},
		"server error": {
			status: http.StatusInternalServerError,
			body:   `{}`,
			want:   "HTTP 500",
			wantIs: oidcclient.ErrRefresh,
		},
		"proxy error page": {
			status: http.StatusBadGateway,
			body:   `<html>502 Bad Gateway</html>`,
			want:   "HTTP 502",
			wantIs: oidcclient.ErrRefresh,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newTokenProvider(t, func(url.Values) (int, string) { return tc.status, tc.body })
			dir := t.TempDir()
			storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

			_, err := oidcSource(t, dir, p.URL, nil).Token()
			require.ErrorIs(t, err, ErrTokenLoad)
			require.ErrorIs(t, err, tc.wantIs)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), loginHint)
			assert.NotContains(t, err.Error(), "stored-refresh")
			assert.NotContains(t, err.Error(), "Token is not active", "error_description is left out")
			assert.Equal(t, "stored-refresh", readBundle(t, dir).RefreshToken, "the file is unchanged")
		})
	}
}

func TestDiskTokenSource_DiscoveryFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	oidcclient.ResetDiscoveryCache()
	dir := t.TempDir()
	storeToken(t, dir, srv.URL, "expired-access", "stored-refresh", -time.Minute)

	_, err := oidcSource(t, dir, srv.URL, nil).Token()
	require.ErrorIs(t, err, ErrTokenLoad)
	require.ErrorIs(t, err, oidcclient.ErrDiscovery)
	assert.Contains(t, err.Error(), "HTTP 404")
}

// TestDiskTokenSource_DiscoveryWaitIsBounded verifies that a discovery
// endpoint that does not answer delays Token by refreshWaitTimeout, not
// by the HTTP client's timeout.
func TestDiskTokenSource_DiscoveryWaitIsBounded(t *testing.T) {
	setRefreshWaitTimeout(t, 100*time.Millisecond)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	oidcclient.ResetDiscoveryCache()
	dir := t.TempDir()
	storeToken(t, dir, srv.URL, "expired-access", "stored-refresh", -time.Minute)

	_, err := tokenWithin(t, oidcSource(t, dir, srv.URL, nil), 5*time.Second)
	require.ErrorIs(t, err, ErrTokenLoad)
	require.ErrorIs(t, err, oidcclient.ErrDiscovery)
}

func TestDiskTokenSource_InvalidGrantUsesPeerToken(t *testing.T) {
	dir := t.TempDir()
	var p *tokenProvider
	p = newTokenProvider(t, func(url.Values) (int, string) {
		// Another process refreshed with the same refresh token first.
		storeTokenFromHandler(t, dir, p.URL, "peer-access", "peer-refresh", time.Hour)
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	})
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "peer-access", tok.AccessToken)
	assert.Len(t, p.requests(), 1)
}

func TestOIDCAuth_ConcurrentRequestsRefreshOnce(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	// Two clients for the same gateway, as two NewClient calls make, each
	// with eight concurrent RPCs. Each client shares one refresh between
	// its RPCs; the clients share the per-directory lock, so the second
	// finds the token the first wrote.
	auths := []types.AuthProvider{oidcAuth(t, dir, p.URL, ""), oidcAuth(t, dir, p.URL, "")}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			md, err := auths[i%2].GetRequestMetadata(context.Background())
			if err == nil && md["authorization"] != "Bearer refreshed-access" {
				err = fmt.Errorf("unexpected token %q", md["authorization"])
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Len(t, p.requests(), 1)
}

func TestOIDCAuth_CanceledRequestStillStoresRotatedToken(t *testing.T) {
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	auth := oidcAuth(t, dir, p.URL, "")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := auth.GetRequestMetadata(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// The provider answers after the RPC gave up. RefreshableToken runs
	// Token apart from the RPC, so the refresh still finishes and stores
	// the rotated token.
	answer()
	require.Eventually(t, func() bool {
		return readBundle(t, dir).RefreshToken == "rotated-refresh"
	}, 5*time.Second, 10*time.Millisecond)

	md, err := auth.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer refreshed-access", md["authorization"])
	assert.Len(t, p.requests(), 1)
}

func TestOIDCAuth_OtherIssuerOrClientDoesNotRefresh(t *testing.T) {
	p := newTokenProvider(t, rotating)
	other := newTokenProvider(t, rotating)

	t.Run("expired, other issuer", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, other.URL, "expired-access", "stored-refresh", -time.Minute)
		_, err := oidcAuth(t, dir, p.URL, "").GetRequestMetadata(context.Background())
		require.ErrorIs(t, err, ErrTokenLoad)
		assert.Contains(t, err.Error(), "another issuer or client than metadata.json names")
		assert.Contains(t, err.Error(), loginHint)
	})
	t.Run("expired, other client", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
		cfg := oidcConfig(dir, p.URL, "")
		cfg.OIDCClientID = "other-client"
		auth, err := resolveAuthProvider(cfg, nil)
		require.NoError(t, err)
		_, err = auth.GetRequestMetadata(context.Background())
		require.ErrorIs(t, err, ErrTokenLoad)
		assert.Contains(t, err.Error(), "another issuer or client than metadata.json names")
	})
	t.Run("expiring, other issuer", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, other.URL, "expiring-access", "stored-refresh", 5*time.Second)
		md, err := oidcAuth(t, dir, p.URL, "").GetRequestMetadata(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Bearer expiring-access", md["authorization"])
	})
	t.Run("trailing slash is the same issuer", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
		md, err := oidcAuth(t, dir, p.URL+"/", "").GetRequestMetadata(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Bearer refreshed-access", md["authorization"])
	})
	assert.Len(t, p.requests(), 1)
	assert.Empty(t, other.requests())
}

func TestDiskTokenSource_ShortLivedTokensAreNotRefreshedOnEveryCall(t *testing.T) {
	// Tokens valid for less than the 30 s window: without the cap at half
	// their lifetime, every call would refresh.
	for _, lifetime := range []int{4, 20} {
		t.Run(fmt.Sprintf("%d s", lifetime), func(t *testing.T) {
			p := newTokenProvider(t, func(url.Values) (int, string) {
				return http.StatusOK, fmt.Sprintf(`{"access_token":"short-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":%d}`, lifetime)
			})
			dir := t.TempDir()
			storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
			src := oidcSource(t, dir, p.URL, nil)

			tok, err := src.Token()
			require.NoError(t, err)
			assert.Equal(t, "short-access", tok.AccessToken)
			for range 5 {
				tok, err = src.Token()
				require.NoError(t, err)
				assert.Equal(t, "short-access", tok.AccessToken)
			}
			assert.Len(t, p.requests(), 1)
		})
	}
}

// TestDiskTokenSource_WindowFollowsGrantedLifetime verifies that the
// window is half the lifetime of the last granted token when that is
// under 60 s, twice the window, and the full 30 s otherwise.
func TestDiskTokenSource_WindowFollowsGrantedLifetime(t *testing.T) {
	for lifetime, want := range map[int]time.Duration{
		4:    2 * time.Second,
		20:   10 * time.Second,
		45:   22500 * time.Millisecond,
		61:   refreshWindow,
		3600: refreshWindow,
	} {
		t.Run(fmt.Sprintf("%d s", lifetime), func(t *testing.T) {
			p := newTokenProvider(t, func(url.Values) (int, string) {
				return http.StatusOK, fmt.Sprintf(`{"access_token":"a","refresh_token":"r","expires_in":%d}`, lifetime)
			})
			dir := t.TempDir()
			storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
			src := oidcSource(t, dir, p.URL, nil)
			assert.Equal(t, refreshWindow, time.Duration(src.RefreshWindow()), "before any grant")

			_, err := src.Token()
			require.NoError(t, err)
			assert.InDelta(t, want.Seconds(), time.Duration(src.RefreshWindow()).Seconds(), 0.5)
		})
	}
}

// TestDiskTokenSource_WindowIgnoresPeerTokens verifies that the window is
// sized by tokens from this source's own token requests, not by the time
// left on a token another caller stored.
func TestDiskTokenSource_WindowIgnoresPeerTokens(t *testing.T) {
	setRefreshWaitTimeout(t, 5*time.Second)

	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	unlock, err := oidcclient.LockDir(context.Background(), dir)
	require.NoError(t, err)
	go func() {
		// A peer stores a token with 40 s left while Token waits.
		time.Sleep(20 * time.Millisecond)
		storeTokenFromHandler(t, dir, p.URL, "peer-access", "peer-refresh", 40*time.Second)
		unlock()
	}()
	src := oidcSource(t, dir, p.URL, nil)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "peer-access", tok.AccessToken)
	assert.Equal(t, refreshWindow, time.Duration(src.RefreshWindow()))
	assert.Empty(t, p.requests())
}

// TestDiskTokenSource_UnwritableDirSpendsNoRefreshToken verifies that a
// token directory the process cannot write to is reported before the
// token request, so a rotating provider does not revoke the stored
// refresh token for a response that could not be kept.
func TestDiskTokenSource_UnwritableDirSpendsNoRefreshToken(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := oidcSource(t, dir, p.URL, nil).Token()
	require.ErrorIs(t, err, ErrTokenLoad)
	require.ErrorIs(t, err, oidcclient.ErrTokenPersist)
	assert.Empty(t, p.requests())
	assert.Equal(t, "stored-refresh", readBundle(t, dir).RefreshToken)
}

// TestOIDCAuth_WriteFailureKeepsRenewedToken verifies that a token the
// provider issued is used, and logged, when it cannot be written: the
// provider has already revoked the refresh token in the file.
func TestOIDCAuth_WriteFailureKeepsRenewedToken(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	dir := t.TempDir()
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		// The directory becomes read-only while the request is in flight.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Errorf("chmod: %v", err)
		}
		return rotating(form)
	})
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	logger := &recordingLogger{}
	auth, err := resolveAuthProvider(oidcConfig(dir, p.URL, ""), logger)
	require.NoError(t, err)

	for range 3 {
		md, err := auth.GetRequestMetadata(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Bearer refreshed-access", md["authorization"])
	}
	assert.Len(t, p.requests(), 1)
	assert.Equal(t, "stored-refresh", readBundle(t, dir).RefreshToken)
	errs := logger.errors()
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], oidcclient.ErrTokenPersist)
}

// TestDiskTokenSource_UnsavedTokenOutlivesWaiters verifies that a renewed
// token that could not be written is kept after every caller stopped
// waiting for it: the provider has revoked the refresh token in the file.
// Token uses it, a renewal does not send the revoked refresh token while
// it still cannot be written, and once the directory is writable again
// the next renewal writes it and refreshes with its refresh token.
func TestDiskTokenSource_UnsavedTokenOutlivesWaiters(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	setRefreshWait(t, 50*time.Millisecond)
	setRefreshRetryDelay(t, 10*time.Millisecond)
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	var calls atomic.Int32
	p := newTokenProvider(t, func(url.Values) (int, string) {
		if calls.Add(1) == 1 {
			// Slower than refreshWait, and the directory becomes read-only
			// before the response arrives.
			time.Sleep(200 * time.Millisecond)
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Errorf("chmod: %v", err)
			}
			// Due in 3 s: the window is half of its 6 s lifetime.
			return http.StatusOK, `{"access_token":"renewed-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":6}`
		}
		return http.StatusOK, `{"access_token":"renewed-2","refresh_token":"rt-2","token_type":"Bearer","expires_in":3600}`
	})
	storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 8*time.Second)
	logger := &recordingLogger{}
	src := oidcSource(t, dir, p.URL, logger)

	tok, err := tokenWithin(t, src, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "usable-access", tok.AccessToken, "the caller stopped waiting")
	src.background.Wait()
	assert.Contains(t, logger.messages(), "cannot store the renewed OIDC token")

	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "renewed-1", tok.AccessToken, "the unwritten token is used")
	assert.Equal(t, "stored-refresh", readBundle(t, dir).RefreshToken)
	require.Len(t, p.requests(), 1)

	// Once renewed-1 is due, renewals cannot write it and send nothing.
	deadline := time.Now().Add(4 * time.Second)
	for len(logger.messages()) < 2 && time.Now().Before(deadline) {
		tok, err = src.Token()
		require.NoError(t, err)
		assert.Equal(t, "renewed-1", tok.AccessToken)
		src.background.Wait()
		time.Sleep(20 * time.Millisecond)
	}
	msgs := logger.messages()
	require.Len(t, msgs, 2, "no renewal of the due token was attempted")
	assert.Equal(t, "OIDC token renewal failed", msgs[1])
	assert.ErrorIs(t, logger.errors()[1], ErrTokenLoad)
	require.Len(t, p.requests(), 1, "the revoked refresh token was sent")

	require.NoError(t, os.Chmod(dir, 0o700))
	time.Sleep(20 * time.Millisecond) // past the retry delay
	_, err = src.Token()
	require.NoError(t, err)
	// renewed-1 is still valid, so on a loaded machine the call may return
	// it after refreshWait while the renewal goes on.
	src.background.Wait()
	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "renewed-2", tok.AccessToken)
	reqs := p.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "rt-1", reqs[1].Get("refresh_token"), "the refresh used the unwritten token's refresh token")
	b := readBundle(t, dir)
	assert.Equal(t, "renewed-2", b.AccessToken)
	assert.Equal(t, "rt-2", b.RefreshToken)
}

// TestDiskTokenSource_UnsavedTokenYieldsToFileChanges verifies that an
// unwritten renewed token is dropped when the file changes, as after a
// login, and is not served after a logout removed the file.
func TestDiskTokenSource_UnsavedTokenYieldsToFileChanges(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	for _, change := range []string{"login", "logout"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			p := newTokenProvider(t, func(form url.Values) (int, string) {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Errorf("chmod: %v", err)
				}
				return rotating(form)
			})
			storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
			src := oidcSource(t, dir, p.URL, nil)

			tok, err := src.Token()
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access", tok.AccessToken)
			src.background.Wait()
			src.mu.Lock()
			require.NotNil(t, src.unsaved, "the unwritten token was not kept")
			src.mu.Unlock()
			require.NoError(t, os.Chmod(dir, 0o700))

			if change == "login" {
				storeToken(t, dir, p.URL, "login-access", "login-refresh", time.Hour)
				for range 2 {
					tok, err = src.Token()
					require.NoError(t, err)
					assert.Equal(t, "login-access", tok.AccessToken)
				}
			} else {
				require.NoError(t, os.Remove(filepath.Join(dir, oidctoken.FileName)))
				_, err = src.Token()
				require.ErrorIs(t, err, ErrTokenLoad)
			}
			assert.Len(t, p.requests(), 1)
			src.mu.Lock()
			defer src.mu.Unlock()
			if change == "login" {
				assert.Nil(t, src.unsaved)
			}
		})
	}
}

// TestDiskTokenSource_SaveUnsaved verifies how a renewal writes an
// unwritten renewed token first: under the refresh lock, only over the
// token it replaced, keeping the file's other fields, and not at all when
// the file cannot be read.
func TestDiskTokenSource_SaveUnsaved(t *testing.T) {
	const issuer = "https://issuer.example"
	unsavedFor := func(src *diskTokenSource, over string) *unsavedToken {
		b := bundleFor(issuer, "unsaved-access", "unsaved-refresh", time.Hour)
		b.ClientCredentials = true
		u := &unsavedToken{bundle: b, over: over, rotated: true}
		src.mu.Lock()
		src.unsaved = u
		src.mu.Unlock()
		return u
	}
	unsaved := func(src *diskTokenSource) *unsavedToken {
		src.mu.Lock()
		defer src.mu.Unlock()
		return src.unsaved
	}

	t.Run("writes over the token it replaced", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, issuer, "old-access", "old-refresh", time.Minute)
		src := oidcSource(t, dir, issuer, nil)
		unsavedFor(src, "old-access")
		_, err := src.saveUnsaved(context.Background())
		require.NoError(t, err)
		b := readBundle(t, dir)
		assert.Equal(t, "unsaved-access", b.AccessToken)
		assert.Equal(t, "unsaved-refresh", b.RefreshToken)
		assert.True(t, b.ClientCredentials)
		assert.Nil(t, unsaved(src))
	})
	t.Run("file changed since", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, issuer, "newer-access", "newer-refresh", time.Hour)
		src := oidcSource(t, dir, issuer, nil)
		unsavedFor(src, "old-access")
		_, err := src.saveUnsaved(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "newer-access", readBundle(t, dir).AccessToken, "the newer token was overwritten")
		assert.Nil(t, unsaved(src))
	})
	t.Run("file removed since", func(t *testing.T) {
		dir := t.TempDir()
		src := oidcSource(t, dir, issuer, nil)
		unsavedFor(src, "old-access")
		_, err := src.saveUnsaved(context.Background())
		require.NoError(t, err)
		_, err = os.Stat(filepath.Join(dir, oidctoken.FileName))
		assert.ErrorIs(t, err, os.ErrNotExist, "the logout was undone")
		assert.Nil(t, unsaved(src))
	})
	t.Run("file unreadable", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, oidctoken.FileName, "{not json")
		src := oidcSource(t, dir, issuer, nil)
		u := unsavedFor(src, "old-access")
		rotated, err := src.saveUnsaved(context.Background())
		require.Error(t, err)
		assert.True(t, rotated)
		data, err := os.ReadFile(filepath.Join(dir, oidctoken.FileName))
		require.NoError(t, err)
		assert.Equal(t, "{not json", string(data))
		assert.Same(t, u, unsaved(src))
	})
	t.Run("waits for the refresh lock", func(t *testing.T) {
		dir := t.TempDir()
		storeToken(t, dir, issuer, "old-access", "old-refresh", time.Minute)
		src := oidcSource(t, dir, issuer, nil)
		u := unsavedFor(src, "old-access")
		unlock, err := oidcclient.LockDir(context.Background(), dir)
		require.NoError(t, err)
		defer unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = src.saveUnsaved(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, "old-access", readBundle(t, dir).AccessToken)
		assert.Same(t, u, unsaved(src))
	})
	t.Run("keepUnsaved", func(t *testing.T) {
		src := oidcSource(t, t.TempDir(), issuer, nil)
		file := bundleFor(issuer, "file-access", "file-refresh", time.Minute)
		renewed := bundleFor(issuer, "new-access", "new-refresh", time.Hour)
		renewed.ClientCredentials = true
		src.keepUnsaved(oidcclient.Result{Granted: true, Unwritten: &renewed, Replaced: &file})
		u := unsaved(src)
		require.NotNil(t, u)
		assert.Equal(t, "file-access", u.over)
		assert.Equal(t, renewed, u.bundle)
		assert.True(t, u.rotated)

		same := bundleFor(issuer, "same-access", "file-refresh", time.Hour)
		src.keepUnsaved(oidcclient.Result{Granted: true, Unwritten: &same, Replaced: &file})
		require.NotNil(t, unsaved(src))
		assert.Equal(t, "same-access", unsaved(src).bundle.AccessToken)
		assert.False(t, unsaved(src).rotated, "the refresh token was not rotated")

		src.keepUnsaved(oidcclient.Result{Granted: true})
		assert.Equal(t, "same-access", unsaved(src).bundle.AccessToken, "a result without Unwritten replaced the kept token")
	})
}

// TestDiskTokenSource_UnsavedTokenLosesToLoginDuringFailedWrite verifies
// that a login that lands right after a failed write, while the renewal
// still reports it, wins over the unwritten token and is not overwritten.
func TestDiskTokenSource_UnsavedTokenLosesToLoginDuringFailedWrite(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Errorf("chmod: %v", err)
		}
		return rotating(form)
	})
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	// The login happens while the renewal reports the failed write.
	logger := &hookLogger{onError: func(msg string) {
		if msg != "cannot store the renewed OIDC token" {
			return
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("chmod: %v", err)
		}
		storeTokenFromHandler(t, dir, p.URL, "login-access", "login-refresh", time.Hour)
	}}
	src := oidcSource(t, dir, p.URL, logger)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "login-access", tok.AccessToken)
	_, err = src.saveUnsaved(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "login-refresh", readBundle(t, dir).RefreshToken)
}

// TestDiskTokenSource_UnsavedTokenWithoutRotation verifies that a failed
// write of an unwritten token stops token requests only when the refresh
// token was rotated: otherwise the one in the file still works.
func TestDiskTokenSource_UnsavedTokenWithoutRotation(t *testing.T) {
	old := writeUnsaved
	writeUnsaved = func(context.Context, string, oidctoken.Bundle, string) error { return errors.New("disk full") }
	t.Cleanup(func() { writeUnsaved = old })
	for _, rotated := range []bool{false, true} {
		t.Run(fmt.Sprint("rotated=", rotated), func(t *testing.T) {
			p := newTokenProvider(t, rotating)
			dir := t.TempDir()
			storeToken(t, dir, p.URL, "file-access", "file-refresh", -time.Minute)
			src := oidcSource(t, dir, p.URL, nil)
			// An unwritten token that has expired, issued in place of the
			// file's token.
			rt := "file-refresh"
			if rotated {
				rt = "unwritten-refresh"
			}
			b := bundleFor(p.URL, "unwritten-access", rt, -time.Second)
			src.mu.Lock()
			src.unsaved = &unsavedToken{bundle: b, over: "file-access", rotated: rotated}
			src.mu.Unlock()

			tok, err := src.Token()
			if rotated {
				require.ErrorIs(t, err, ErrTokenLoad)
				assert.ErrorContains(t, err, "disk full")
				assert.Empty(t, p.requests(), "the revoked refresh token was sent")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access", tok.AccessToken)
			reqs := p.requests()
			require.Len(t, reqs, 1)
			assert.Equal(t, "file-refresh", reqs[0].Get("refresh_token"))
		})
	}
}

// hookLogger calls onError with the message of each error it is given.
type hookLogger struct{ onError func(msg string) }

func (l *hookLogger) Debug(string, ...any)                {}
func (l *hookLogger) Info(string, ...any)                 {}
func (l *hookLogger) Error(_ error, msg string, _ ...any) { l.onError(msg) }

// TestDiskTokenSource_UsableTokenWaitsBriefly verifies that a token that
// is due but has not expired is returned within refreshWait, whatever the
// renewal does: the provider fails, hangs, or another renewal holds the
// lock.
func TestDiskTokenSource_UsableTokenWaitsBriefly(t *testing.T) {
	setRefreshWait(t, 100*time.Millisecond)

	t.Run("provider fails", func(t *testing.T) {
		p := newTokenProvider(t, unavailable)
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 8*time.Second)
		logger := &recordingLogger{}
		src := oidcSource(t, dir, p.URL, logger)

		tok, err := tokenWithin(t, src, 2*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "usable-access", tok.AccessToken)
		src.background.Wait()

		// The failure is logged, and the next calls within
		// refreshRetryDelay neither fail nor ask the provider again.
		errs := logger.errors()
		require.Len(t, errs, 1)
		assert.ErrorIs(t, errs[0], ErrTokenLoad)
		assert.Contains(t, errs[0].Error(), "HTTP 503")
		assert.Equal(t, []string{"OIDC token renewal failed"}, logger.messages())
		assert.Equal(t, []any{"gateway", "prod"}, logger.lastKeysAndValues())
		for range 3 {
			tok, err = src.Token()
			require.NoError(t, err)
			assert.Equal(t, "usable-access", tok.AccessToken)
		}
		src.background.Wait()
		assert.Len(t, p.requests(), 1)
	})
	t.Run("provider hangs", func(t *testing.T) {
		release := make(chan struct{})
		p := newTokenProvider(t, func(form url.Values) (int, string) {
			<-release
			return rotating(form)
		})
		answer := sync.OnceFunc(func() { close(release) })
		defer answer()
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 8*time.Second)
		src := oidcSource(t, dir, p.URL, nil)

		// Both calls return the current token; the second joins the
		// renewal the first started.
		for range 2 {
			tok, err := tokenWithin(t, src, 2*time.Second)
			require.NoError(t, err)
			assert.Equal(t, "usable-access", tok.AccessToken)
		}
		answer()
		src.background.Wait()
		assert.Len(t, p.requests(), 1)
	})
	t.Run("another renewal holds the lock", func(t *testing.T) {
		p := newTokenProvider(t, rotating)
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 8*time.Second)
		unlock, err := oidcclient.LockDir(context.Background(), dir)
		require.NoError(t, err)
		src := oidcSource(t, dir, p.URL, nil)
		defer unlock()

		tok, err := tokenWithin(t, src, 2*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "usable-access", tok.AccessToken)
	})
}

func TestDiskTokenSource_LockWaitTimeout(t *testing.T) {
	hold := func(t *testing.T, dir string) {
		t.Helper()
		unlock, err := oidcclient.LockDir(context.Background(), dir)
		require.NoError(t, err)
		t.Cleanup(unlock)
	}
	// writeLater writes b while Token waits for the lock: well after Token
	// has started waiting, well before refreshWaitTimeout ends.
	writeLater := func(dir string, b oidctoken.Bundle) <-chan error {
		written := make(chan error, 1)
		go func() {
			time.Sleep(100 * time.Millisecond)
			written <- oidcclient.WriteToken(dir, b)
		}()
		return written
	}

	t.Run("reports the timeout", func(t *testing.T) {
		setRefreshWaitTimeout(t, 50*time.Millisecond)
		p := newTokenProvider(t, rotating)
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
		hold(t, dir)

		_, err := tokenWithin(t, oidcSource(t, dir, p.URL, nil), 5*time.Second)
		require.ErrorIs(t, err, ErrTokenLoad)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "waiting for another refresh of "+oidctoken.FileName)
		assert.Empty(t, p.requests())
	})

	t.Run("uses a token written meanwhile", func(t *testing.T) {
		setRefreshWaitTimeout(t, time.Second)
		p := newTokenProvider(t, rotating)
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
		hold(t, dir)
		// The CLI keeps the issuer as the user typed it.
		written := writeLater(dir, bundleFor(p.URL+"/", "written-meanwhile", "peer-refresh", time.Hour))

		tok, err := tokenWithin(t, oidcSource(t, dir, p.URL, nil), 5*time.Second)
		require.NoError(t, <-written)
		require.NoError(t, err)
		assert.Equal(t, "written-meanwhile", tok.AccessToken)
		assert.Empty(t, p.requests())
	})

	t.Run("ignores a token written meanwhile for another client", func(t *testing.T) {
		setRefreshWaitTimeout(t, time.Second)
		p := newTokenProvider(t, rotating)
		dir := t.TempDir()
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
		hold(t, dir)
		b := bundleFor(p.URL, "other-access", "other-refresh", time.Hour)
		b.ClientID = "other-client"
		written := writeLater(dir, b)

		_, err := tokenWithin(t, oidcSource(t, dir, p.URL, nil), 5*time.Second)
		require.NoError(t, <-written)
		require.ErrorIs(t, err, ErrTokenLoad)
		// The lock wait timed out and the fallback rejected the token;
		// a write that came first would fail on the client check instead.
		assert.Contains(t, err.Error(), "waiting for another refresh")
	})
}

// TestDiskTokenSource_ValidTokenSkipsRefreshLock verifies that a token
// outside the refresh window is returned without waiting for a refresh of
// the same directory that is in progress.
func TestDiskTokenSource_ValidTokenSkipsRefreshLock(t *testing.T) {
	p := newTokenProvider(t, rotating)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "valid-access", "stored-refresh", time.Hour)
	unlock, err := oidcclient.LockDir(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(unlock)

	tok, err := tokenWithin(t, oidcSource(t, dir, p.URL, nil), 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "valid-access", tok.AccessToken)
	assert.Empty(t, p.requests())
}

// TestDiskTokenSource_GrantOutlivesWaitTimeout verifies that a token
// request already sent is not cut off by refreshWaitTimeout: the response
// is used and the rotated refresh token is stored.
func TestDiskTokenSource_GrantOutlivesWaitTimeout(t *testing.T) {
	setRefreshWaitTimeout(t, 100*time.Millisecond)

	p := newTokenProvider(t, func(form url.Values) (int, string) {
		time.Sleep(3 * refreshWaitTimeout)
		return rotating(form)
	})
	// Discover now, so that refreshWaitTimeout covers only the lock wait.
	_, err := oidcclient.Discover(context.Background(), p.URL)
	require.NoError(t, err)
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	assert.Equal(t, "rotated-refresh", readBundle(t, dir).RefreshToken)
	assert.Len(t, p.requests(), 1)
}

// TestDiskTokenSource_RefreshWithoutExpiresIn verifies that a refreshed
// token without expires_in is stored without an expiry and is then used
// as is, without another refresh.
func TestDiskTokenSource_RefreshWithoutExpiresIn(t *testing.T) {
	p := newTokenProvider(t, func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"no-expiry-access","refresh_token":"rotated-refresh","token_type":"Bearer"}`
	})
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)
	src := oidcSource(t, dir, p.URL, nil)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "no-expiry-access", tok.AccessToken)
	assert.True(t, tok.Expiry.IsZero())
	assert.Nil(t, readBundle(t, dir).ExpiresAt)

	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "no-expiry-access", tok.AccessToken)
	assert.Len(t, p.requests(), 1)
}

// TestDiskTokenSource_WaitIsPerRenewal verifies that while one renewal
// hangs, only the calls within refreshWait of its start wait for it; later
// calls return the current token at once.
func TestDiskTokenSource_WaitIsPerRenewal(t *testing.T) {
	setRefreshWait(t, time.Second)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 20*time.Second)
	src := oidcSource(t, dir, p.URL, nil)

	start := time.Now()
	tok, err := tokenWithin(t, src, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "usable-access", tok.AccessToken)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond, "the first call waits for the renewal")
	for range 3 {
		tok, err := tokenWithin(t, src, 500*time.Millisecond)
		require.NoError(t, err)
		assert.Equal(t, "usable-access", tok.AccessToken)
	}
	answer()
	src.background.Wait()
	assert.Len(t, p.requests(), 1)
}

// TestDiskTokenSource_TokenExpiringDuringWaitIsRenewed verifies that a
// token that expires while Token waits for its renewal is not returned:
// Token waits for the renewal instead.
func TestDiskTokenSource_TokenExpiringDuringWaitIsRenewed(t *testing.T) {
	setRefreshWait(t, 10*time.Second)
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		// Answer only after the stored token has expired.
		time.Sleep(1600 * time.Millisecond)
		return rotating(form)
	})
	dir := t.TempDir()
	// The file keeps whole seconds, so this expires 0.5 to 1.5 s from now.
	storeToken(t, dir, p.URL, "expiring-access", "stored-refresh", 1500*time.Millisecond)

	tok, err := tokenWithin(t, oidcSource(t, dir, p.URL, nil), 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
}

// TestDiskTokenSource_ExpiredTokenUsesPeerRenewal verifies that when the
// renewal of an expired token fails because another process renewed the
// file meanwhile, the token it wrote is used while it has not expired,
// even if it is already inside the window.
func TestDiskTokenSource_ExpiredTokenUsesPeerRenewal(t *testing.T) {
	dir := t.TempDir()
	var p *tokenProvider
	p = newTokenProvider(t, func(form url.Values) (int, string) {
		// A peer stores a 20 s token while this request is in flight.
		storeTokenFromHandler(t, dir, p.URL, "peer-access", "peer-refresh", 20*time.Second)
		return rotating(form)
	})
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "peer-access", tok.AccessToken)
	assert.Equal(t, "peer-refresh", readBundle(t, dir).RefreshToken, "the peer's token stands")
}

// TestDiskTokenSource_RetryDelay verifies that after a failed renewal of a
// usable token no renewal starts within refreshRetryDelay, that an expired
// token is renewed regardless, and that a successful renewal ends the
// delay.
func TestDiskTokenSource_RetryDelay(t *testing.T) {
	setRefreshRetryDelay(t, time.Hour)
	var failing atomic.Bool
	failing.Store(true)
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		if failing.Load() {
			return unavailable(form)
		}
		return rotating(form)
	})
	dir := t.TempDir()
	storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 8*time.Second)
	src := oidcSource(t, dir, p.URL, nil)

	for range 2 {
		tok, err := src.Token()
		require.NoError(t, err)
		assert.Equal(t, "usable-access", tok.AccessToken)
		src.background.Wait()
	}
	require.Len(t, p.requests(), 1, "a renewal started within the retry delay")

	failing.Store(false)
	storeToken(t, dir, p.URL, "expired-access", "second-refresh", -time.Minute)
	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken, "an expired token waits out no delay")
	require.Len(t, p.requests(), 2)

	storeToken(t, dir, p.URL, "usable-access-2", "third-refresh", 8*time.Second)
	tok, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken, "the successful renewal ended the delay")
	assert.Len(t, p.requests(), 3)
}

// TestDiskTokenSource_ExpiredTokenUsesPeerTokenWithoutRotation verifies
// that the token another process wrote is used after a failed renewal even
// when the provider does not rotate refresh tokens, so the file keeps the
// same refresh token.
func TestDiskTokenSource_ExpiredTokenUsesPeerTokenWithoutRotation(t *testing.T) {
	dir := t.TempDir()
	var p *tokenProvider
	p = newTokenProvider(t, func(url.Values) (int, string) {
		// A peer renews with the same refresh token, then the provider
		// fails this request.
		storeTokenFromHandler(t, dir, p.URL, "peer-access", "stored-refresh", 20*time.Second)
		return http.StatusServiceUnavailable, `{}`
	})
	storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

	tok, err := oidcSource(t, dir, p.URL, nil).Token()
	require.NoError(t, err)
	assert.Equal(t, "peer-access", tok.AccessToken)
}

// TestRefreshDefaults pins the values the documentation states.
func TestRefreshDefaults(t *testing.T) {
	assert.Equal(t, 30*time.Second, refreshWindow, "renewal window")
	assert.Equal(t, 2*time.Second, refreshWait, "wait for a renewal while the token is usable")
	assert.Equal(t, 5*time.Second, refreshRetryDelay, "delay after a failed renewal of a usable token")
	assert.Equal(t, 30*time.Second, refreshWaitTimeout, "bound on the lock wait and discovery")
}

// TestDiskTokenSource_ExpiredRenewalFallback verifies what an expired
// token's failed renewal falls back to: an expired token another process
// wrote is not used, and a logout during the renewal leaves an error, not
// a token.
func TestDiskTokenSource_ExpiredRenewalFallback(t *testing.T) {
	t.Run("expired peer token", func(t *testing.T) {
		dir := t.TempDir()
		var p *tokenProvider
		p = newTokenProvider(t, func(url.Values) (int, string) {
			storeTokenFromHandler(t, dir, p.URL, "peer-expired-access", "peer-refresh", -time.Second)
			return http.StatusServiceUnavailable, `{}`
		})
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

		tok, err := oidcSource(t, dir, p.URL, nil).Token()
		require.ErrorIs(t, err, ErrTokenLoad, "got token %v", tok)
		assert.Contains(t, err.Error(), loginHint)
	})
	t.Run("logout during the renewal", func(t *testing.T) {
		dir := t.TempDir()
		p := newTokenProvider(t, func(form url.Values) (int, string) {
			if err := os.Remove(filepath.Join(dir, oidctoken.FileName)); err != nil {
				t.Errorf("remove: %v", err)
			}
			return rotating(form)
		})
		storeToken(t, dir, p.URL, "expired-access", "stored-refresh", -time.Minute)

		tok, err := oidcSource(t, dir, p.URL, nil).Token()
		require.ErrorIs(t, err, ErrTokenLoad, "got token %v", tok)
		assert.Contains(t, err.Error(), "changed during the refresh")
		_, statErr := os.Stat(filepath.Join(dir, oidctoken.FileName))
		assert.True(t, os.IsNotExist(statErr), "the logout was undone")
	})
}
