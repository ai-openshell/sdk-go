// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/gateway"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// refreshProvider is a mock OIDC provider whose token endpoint records
// every request form and answers through respond.
type refreshProvider struct {
	*httptest.Server
	mu    sync.Mutex
	forms []url.Values
}

func (p *refreshProvider) grants() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	grants := make([]string, 0, len(p.forms))
	for _, form := range p.forms {
		grants = append(grants, form.Get("grant_type"))
	}
	return grants
}

func (p *refreshProvider) form(i int) url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forms[i]
}

func newRefreshProvider(t *testing.T, respond func(form url.Values) (int, string)) *refreshProvider {
	t.Helper()
	p := &refreshProvider{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           p.URL,
			"authorization_endpoint":           p.URL + "/authorize",
			"token_endpoint":                   p.URL + "/token",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.forms = append(p.forms, r.PostForm)
		p.mu.Unlock()
		status, body := respond(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// refreshed answers refresh_token grants with a rotated token pair and
// authorization_code grants with the interactive flow's token.
func refreshed(form url.Values) (int, string) {
	if form.Get("grant_type") == "refresh_token" {
		return http.StatusOK, tokenResponseJSON("refreshed-access-token", "rotated-refresh-token", 3600)
	}
	return http.StatusOK, tokenResponseJSON("login-access-token", "login-refresh-token", 3600)
}

const invalidGrantJSON = `{"error":"invalid_grant","error_description":"refresh token already used"}`

func writeStoredToken(t *testing.T, dir, issuer string, tok *oauth2.Token) {
	t.Helper()
	require.NoError(t, writeToken(dir, oidctoken.FromToken(tok, issuer, "test-client")))
}

// writeStoredTokenFromHandler writes a token from an HTTP handler, where
// require must not be used.
func writeStoredTokenFromHandler(t *testing.T, dir, issuer string, tok *oauth2.Token) {
	t.Helper()
	if err := writeToken(dir, oidctoken.FromToken(tok, issuer, "test-client")); err != nil {
		t.Errorf("write peer token: %v", err)
	}
}

func expiredToken(refresh string) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: refresh,
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}
}

func readStoredBundle(t *testing.T, dir string) oidctoken.Bundle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, oidctoken.FileName))
	require.NoError(t, err)
	var b oidctoken.Bundle
	require.NoError(t, json.Unmarshal(data, &b))
	return b
}

// loginNoPrompt runs Login with the keyboard flow and no input, so any
// interactive prompt makes it fail.
func loginNoPrompt(t *testing.T, issuer, dir string, opts ...LoginOption) (*oauth2.Token, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return loginNoPromptCtx(ctx, issuer, dir, opts...)
}

func loginNoPromptCtx(ctx context.Context, issuer, dir string, opts ...LoginOption) (*oauth2.Token, string, error) {
	var prompt bytes.Buffer
	opts = append([]LoginOption{
		WithIssuer(issuer),
		WithClientID("test-client"),
		withTokenDir(dir),
		WithKeyboardFlow(),
		withInput(strings.NewReader("")),
		withOutput(&prompt),
	}, opts...)
	tok, err := Login(ctx, "", opts...)
	return tok, prompt.String(), err
}

// TestLogin_RefreshesExpiredToken verifies that Login renews an expired
// stored token with its refresh token, without prompting, and rewrites the
// file in the CLI format (NVIDIA/OpenShell#4514).
func TestLogin_RefreshesExpiredToken(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	tok, prompt, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	assert.Equal(t, "refreshed-access-token", tok.AccessToken)
	assert.Equal(t, "rotated-refresh-token", tok.RefreshToken)

	require.Equal(t, []string{"refresh_token"}, provider.grants())
	form := provider.form(0)
	assert.Equal(t, "still-good", form.Get("refresh_token"))
	assert.Equal(t, "test-client", form.Get("client_id"))
	assert.False(t, form.Has("scope"), "default scopes are not sent on refresh")

	stored := readStoredBundle(t, dir)
	assert.Equal(t, "refreshed-access-token", stored.AccessToken)
	assert.Equal(t, "rotated-refresh-token", stored.RefreshToken)
	assert.Equal(t, provider.URL, stored.Issuer)
	assert.Equal(t, "test-client", stored.ClientID)
	require.NotNil(t, stored.ExpiresAt)
	assert.Greater(t, int64(*stored.ExpiresAt), time.Now().Unix())
}

// TestLogin_RefreshKeepsRefreshTokenWhenNotRotated verifies that a
// provider response without a refresh token keeps the stored one.
func TestLogin_RefreshKeepsRefreshTokenWhenNotRotated(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"refreshed-access-token","token_type":"Bearer","expires_in":3600}`
	})
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("long-lived-refresh"))

	tok, _, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Equal(t, "long-lived-refresh", tok.RefreshToken)
	assert.Equal(t, "long-lived-refresh", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_RefreshSendsConfiguredScopes verifies that scopes set with
// WithScopes are sent on refresh, normalized with openid first as in the
// authorization request.
func TestLogin_RefreshSendsConfiguredScopes(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	_, _, err := loginNoPrompt(t, provider.URL, dir, WithScopes("sandbox:read"))
	require.NoError(t, err)
	assert.Equal(t, "openid sandbox:read", provider.form(0).Get("scope"))
}

// TestLogin_RefreshInvalidGrantUsesPeerToken verifies that when the
// refresh token was already used by another process, Login re-reads the
// file and returns the peer's valid token without a second request.
func TestLogin_RefreshInvalidGrantUsesPeerToken(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(url.Values) (int, string) {
		// Another process refreshed first and rotated the token.
		writeStoredTokenFromHandler(t, dir, provider.URL, &oauth2.Token{
			AccessToken:  "peer-access-token",
			RefreshToken: "peer-refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		})
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	tok, prompt, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	assert.Equal(t, "peer-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token"}, provider.grants())
	assert.Equal(t, "peer-refresh-token", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_RefreshInvalidGrantRetriesWithPeerRefreshToken verifies that
// Login retries once with a different refresh token found on disk when
// the peer's access token has also expired.
func TestLogin_RefreshInvalidGrantRetriesWithPeerRefreshToken(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(form url.Values) (int, string) {
		if form.Get("refresh_token") == "peer-refresh-token" {
			return refreshed(form)
		}
		writeStoredTokenFromHandler(t, dir, provider.URL, expiredToken("peer-refresh-token"))
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	tok, prompt, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	assert.Equal(t, "refreshed-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token", "refresh_token"}, provider.grants())
	assert.Equal(t, "peer-refresh-token", provider.form(1).Get("refresh_token"))
	assert.Equal(t, "rotated-refresh-token", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_RefreshFailureFallsBackToFlow verifies that a rejected refresh
// with no newer token on disk falls back to the interactive flow.
func TestLogin_RefreshFailureFallsBackToFlow(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, func(form url.Values) (int, string) {
		if form.Get("grant_type") == "refresh_token" {
			return http.StatusBadRequest, invalidGrantJSON
		}
		return refreshed(form)
	})
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("revoked"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := Login(ctx, "",
		WithIssuer(provider.URL),
		WithClientID("test-client"),
		withTokenDir(dir),
		WithKeyboardFlow(),
		withInput(strings.NewReader("fake-auth-code\n")),
		withOutput(&bytes.Buffer{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "login-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token", "authorization_code"}, provider.grants())
	assert.Equal(t, "login-refresh-token", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_InMemoryDoesNotRefresh verifies that WithInMemory runs the
// interactive flow without refreshing: a rotated refresh token would not be
// written back, and the file would keep a revoked one.
func TestLogin_InMemoryDoesNotRefresh(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := Login(ctx, "",
		WithIssuer(provider.URL),
		WithClientID("test-client"),
		withTokenDir(dir),
		WithKeyboardFlow(),
		WithInMemory(),
		withInput(strings.NewReader("fake-auth-code\n")),
		withOutput(&bytes.Buffer{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "login-access-token", tok.AccessToken)
	assert.Equal(t, []string{"authorization_code"}, provider.grants())
	assert.Equal(t, "still-good", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_RefreshKeepsClientCredentialsMarker verifies that rewriting the
// file after a refresh keeps the CLI's client_credentials marker.
func TestLogin_RefreshKeepsClientCredentialsMarker(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	b := oidctoken.FromToken(expiredToken("still-good"), provider.URL, "test-client")
	b.ClientCredentials = true
	require.NoError(t, writeToken(dir, b))

	_, _, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	stored := readStoredBundle(t, dir)
	assert.Equal(t, "refreshed-access-token", stored.AccessToken)
	assert.True(t, stored.ClientCredentials)
}

// TestLogin_RefreshPersistFailure verifies that Login returns
// ErrTokenPersist and no token when the refreshed token cannot be written.
func TestLogin_RefreshPersistFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory")
	}
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	tok, _, err := loginNoPrompt(t, provider.URL, dir)
	require.ErrorIs(t, err, ErrTokenPersist)
	assert.Nil(t, tok)
	assert.Equal(t, "still-good", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_RefreshUsesGatewayScopes verifies that the gateway's
// oidc_scopes from metadata.json are sent on refresh.
func TestLogin_RefreshUsesGatewayScopes(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := Login(ctx, "gw",
		withGatewayResolver(func(string) (*gateway.Config, error) {
			return &gateway.Config{
				Name:         "gw",
				Dir:          dir,
				OIDCIssuer:   provider.URL,
				OIDCClientID: "test-client",
				OIDCScopes:   "sandbox:read sandbox:write",
			}, nil
		}),
		WithKeyboardFlow(),
		withInput(strings.NewReader("")),
		withOutput(&bytes.Buffer{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access-token", tok.AccessToken)
	assert.Equal(t, "openid sandbox:read sandbox:write", provider.form(0).Get("scope"))
}

// TestLogin_RefreshInvalidGrantIgnoresPeerForOtherClient verifies that a
// token written for another client is never returned or refreshed.
func TestLogin_RefreshInvalidGrantIgnoresPeerForOtherClient(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(form url.Values) (int, string) {
		if form.Get("grant_type") != "refresh_token" {
			return refreshed(form)
		}
		peer := oidctoken.FromToken(&oauth2.Token{
			AccessToken:  "other-client-access-token",
			RefreshToken: "other-client-refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}, provider.URL, "other-client")
		if err := writeToken(dir, peer); err != nil {
			t.Errorf("write peer token: %v", err)
		}
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := Login(ctx, "",
		WithIssuer(provider.URL),
		WithClientID("test-client"),
		withTokenDir(dir),
		WithKeyboardFlow(),
		withInput(strings.NewReader("fake-auth-code\n")),
		withOutput(&bytes.Buffer{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "login-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token", "authorization_code"}, provider.grants())
}

// TestLogin_RefreshRejectsUnusableResponses verifies that a refresh
// response without an access token, or with a non-positive expires_in, is
// rejected and Login runs the interactive flow.
func TestLogin_RefreshRejectsUnusableResponses(t *testing.T) {
	for name, body := range map[string]string{
		"no access token":   `{"token_type":"Bearer","expires_in":3600}`,
		"zero expires_in":   `{"access_token":"a","token_type":"Bearer","expires_in":0}`,
		"negative lifetime": `{"access_token":"a","token_type":"Bearer","expires_in":-5}`,
	} {
		t.Run(name, func(t *testing.T) {
			resetDiscoveryCache()
			provider := newRefreshProvider(t, func(form url.Values) (int, string) {
				if form.Get("grant_type") == "refresh_token" {
					return http.StatusOK, body
				}
				return refreshed(form)
			})
			dir := t.TempDir()
			writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tok, err := Login(ctx, "",
				WithIssuer(provider.URL),
				WithClientID("test-client"),
				withTokenDir(dir),
				WithKeyboardFlow(),
				withInput(strings.NewReader("fake-auth-code\n")),
				withOutput(&bytes.Buffer{}),
			)
			require.NoError(t, err)
			assert.Equal(t, "login-access-token", tok.AccessToken)
			assert.Equal(t, []string{"refresh_token", "authorization_code"}, provider.grants())
		})
	}
}

// TestLogin_RefreshKeepsStoredIssuerForm verifies that the rewritten file
// keeps the issuer exactly as stored, so a trailing slash written by the
// CLI survives.
func TestLogin_RefreshKeepsStoredIssuerForm(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL+"/", expiredToken("still-good"))

	_, _, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Equal(t, provider.URL+"/", readStoredBundle(t, dir).Issuer)
}

// TestLogin_RefreshDiscoveryFailure verifies that Login reports a discovery
// failure after a single discovery request and without prompting.
func TestLogin_RefreshDiscoveryFailure(t *testing.T) {
	resetDiscoveryCache()
	var discoveries sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveries.Lock()
		calls++
		discoveries.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeStoredToken(t, dir, srv.URL, expiredToken("still-good"))

	_, prompt, err := loginNoPrompt(t, srv.URL, dir)
	require.ErrorIs(t, err, ErrDiscovery)
	assert.Empty(t, prompt)
	discoveries.Lock()
	defer discoveries.Unlock()
	assert.Equal(t, 1, calls)
}

// TestLogin_RefreshContextDeadline verifies that a refresh cut short by the
// caller's deadline returns ErrTimeout and does not start the flow.
func TestLogin_RefreshContextDeadline(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, func(form url.Values) (int, string) {
		time.Sleep(300 * time.Millisecond)
		return refreshed(form)
	})
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))
	// Warm the discovery cache so the deadline falls inside the refresh.
	_, err := discover(context.Background(), provider.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, prompt, err := loginNoPromptCtx(ctx, provider.URL, dir)
	require.ErrorIs(t, err, ErrTimeout)
	assert.Empty(t, prompt)
	assert.Equal(t, "still-good", readStoredBundle(t, dir).RefreshToken)
}

// TestLogin_ConcurrentRefreshSpendsRefreshTokenOnce verifies that
// concurrent Login calls in one process refresh once: the others wait,
// re-read the file and use the token the first one wrote. The mock
// provider rejects a reused refresh token, as a rotating provider does.
func TestLogin_ConcurrentRefreshSpendsRefreshTokenOnce(t *testing.T) {
	resetDiscoveryCache()
	var used sync.Map
	provider := newRefreshProvider(t, func(form url.Values) (int, string) {
		if _, spent := used.LoadOrStore(form.Get("refresh_token"), true); spent {
			return http.StatusBadRequest, invalidGrantJSON
		}
		return refreshed(form)
	})
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	const callers = 4
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, _, err := loginNoPrompt(t, provider.URL, dir)
			errs[i] = err
			if tok != nil {
				results[i] = tok.AccessToken
			}
		}()
	}
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		assert.Equal(t, "refreshed-access-token", results[i])
	}
	assert.Equal(t, []string{"refresh_token"}, provider.grants())
}

// TestLogin_RefreshDiscoveryDeadline verifies that a deadline reached
// during discovery for the refresh returns ErrTimeout without prompting.
func TestLogin_RefreshDiscoveryDeadline(t *testing.T) {
	resetDiscoveryCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
		}
		http.Error(w, "slow", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeStoredToken(t, dir, srv.URL, expiredToken("still-good"))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, prompt, err := loginNoPromptCtx(ctx, srv.URL, dir)
	require.ErrorIs(t, err, ErrTimeout)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, prompt)
}

// TestLogin_RefreshInvalidGrantUsesPeerTokenWithoutRefreshToken verifies
// that a valid token another process wrote is used even when it carries no
// refresh token.
func TestLogin_RefreshInvalidGrantUsesPeerTokenWithoutRefreshToken(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(url.Values) (int, string) {
		writeStoredTokenFromHandler(t, dir, provider.URL, &oauth2.Token{
			AccessToken: "peer-access-token",
			Expiry:      time.Now().Add(time.Hour),
		})
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	tok, prompt, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	assert.Equal(t, "peer-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token"}, provider.grants())
}

// TestLogin_RefreshCanceledContext verifies that a caller who cancels the
// context gets ErrTimeout wrapping context.Canceled, with no prompt and no
// change to the file.
func TestLogin_RefreshCanceledContext(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, prompt, err := loginNoPromptCtx(ctx, provider.URL, dir)
	require.ErrorIs(t, err, ErrTimeout)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, prompt)
	assert.Empty(t, provider.grants())
	assert.Equal(t, "still-good", readStoredBundle(t, dir).RefreshToken)
}

// TestRefreshToken_Errors verifies that every failure of the refresh
// request is reported as errTokenRefresh, is not mistaken for
// invalid_grant, and never includes the refresh token.
func TestRefreshToken_Errors(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL + "/token"
	closed.Close()

	for name, tc := range map[string]struct {
		endpoint func(t *testing.T) string
		want     string
	}{
		"invalid endpoint URL": {
			endpoint: func(*testing.T) string { return "http://[::1]:namedport/token" },
			want:     "failed to create token request",
		},
		"transport failure": {
			endpoint: func(*testing.T) string { return closedURL },
			want:     "token request failed",
		},
		"truncated response body": {
			endpoint: func(t *testing.T) string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					conn, buf, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"access")
					_ = buf.Flush()
					_ = conn.Close()
				}))
				t.Cleanup(srv.Close)
				return srv.URL + "/token"
			},
			want: "failed to read token response",
		},
		"invalid JSON": {
			endpoint: func(t *testing.T) string {
				return newRefreshProvider(t, func(url.Values) (int, string) {
					return http.StatusOK, "not json"
				}).URL + "/token"
			},
			want: "invalid token response JSON",
		},
		"server error without error code": {
			endpoint: func(t *testing.T) string {
				return newRefreshProvider(t, func(url.Values) (int, string) {
					return http.StatusInternalServerError, `{"access_token":"a"}`
				}).URL + "/token"
			},
			want: "HTTP 500",
		},
		"error code with status 401": {
			endpoint: func(t *testing.T) string {
				return newRefreshProvider(t, func(url.Values) (int, string) {
					return http.StatusUnauthorized, `{"error":"invalid_client"}`
				}).URL + "/token"
			},
			want: ": invalid_client",
		},
		"error code with status 200": {
			endpoint: func(t *testing.T) string {
				return newRefreshProvider(t, func(url.Values) (int, string) {
					return http.StatusOK, `{"error":"temporarily_unavailable","access_token":"a"}`
				}).URL + "/token"
			},
			want: ": temporarily_unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := refreshToken(context.Background(), tc.endpoint(t), "test-client", "secret-refresh-token", nil)
			require.ErrorIs(t, err, errTokenRefresh)
			assert.NotErrorIs(t, err, errInvalidGrant)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "secret-refresh-token")
		})
	}
}

// TestRefreshStoredToken_RereadsFileUnderLock verifies that
// refreshStoredToken decides from the file it reads under the refresh
// lock. The bundle Login read before waiting for the lock only decides
// whether to try.
func TestRefreshStoredToken_RereadsFileUnderLock(t *testing.T) {
	valid := func(issuer, clientID string) oidctoken.Bundle {
		return oidctoken.FromToken(&oauth2.Token{
			AccessToken:  "fresh-access-token",
			RefreshToken: "fresh-refresh-token",
			Expiry:       time.Now().Add(time.Hour),
		}, issuer, clientID)
	}
	for name, tc := range map[string]struct {
		file       func(issuer string) *oidctoken.Bundle
		wantToken  string
		wantGrants []string
		wantSent   string
	}{
		"token refreshed by another caller": {
			file: func(issuer string) *oidctoken.Bundle {
				b := valid(issuer, "test-client")
				return &b
			},
			wantToken: "fresh-access-token",
		},
		"newer refresh token on disk": {
			file: func(issuer string) *oidctoken.Bundle {
				b := oidctoken.FromToken(expiredToken("newer-refresh-token"), issuer, "test-client")
				return &b
			},
			wantToken:  "refreshed-access-token",
			wantGrants: []string{"refresh_token"},
			wantSent:   "newer-refresh-token",
		},
		"token for another client": {
			file: func(issuer string) *oidctoken.Bundle {
				b := valid(issuer, "other-client")
				return &b
			},
		},
		"no refresh token on disk": {
			file: func(issuer string) *oidctoken.Bundle {
				b := oidctoken.FromToken(expiredToken(""), issuer, "test-client")
				return &b
			},
		},
		"file removed": {
			file: func(string) *oidctoken.Bundle { return nil },
		},
	} {
		t.Run(name, func(t *testing.T) {
			resetDiscoveryCache()
			provider := newRefreshProvider(t, refreshed)
			dir := t.TempDir()
			if b := tc.file(provider.URL); b != nil {
				require.NoError(t, writeToken(dir, *b))
			}
			cfg := &loginConfig{issuer: provider.URL, clientID: "test-client"}
			// Login read this bundle before it waited for the lock.
			before := oidctoken.FromToken(expiredToken("older-refresh-token"), provider.URL, "test-client")

			tok, err := refreshStoredToken(context.Background(), cfg, dir, before)
			require.NoError(t, err)
			if tc.wantToken == "" {
				assert.Nil(t, tok)
			} else {
				require.NotNil(t, tok)
				assert.Equal(t, tc.wantToken, tok.AccessToken)
			}
			if tc.wantGrants == nil {
				assert.Empty(t, provider.grants())
			} else {
				assert.Equal(t, tc.wantGrants, provider.grants())
			}
			if tc.wantSent != "" {
				assert.Equal(t, tc.wantSent, provider.form(0).Get("refresh_token"))
			}
		})
	}
}

// TestLogin_RefreshLockWaitHonorsContext verifies that a Login waiting for
// another refresh of the same file returns ErrTimeout when its own context
// ends, without prompting.
func TestLogin_RefreshLockWaitHonorsContext(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, refreshed)
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))
	unlock, err := lockTokenDir(context.Background(), dir)
	require.NoError(t, err)
	defer unlock()

	type result struct {
		prompt string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, prompt, err := loginNoPromptCtx(ctx, provider.URL, dir)
		done <- result{prompt, err}
	}()
	select {
	case res := <-done:
		require.ErrorIs(t, res.err, ErrTimeout)
		require.ErrorIs(t, res.err, context.DeadlineExceeded)
		assert.Empty(t, res.prompt)
		assert.Empty(t, provider.grants())
	case <-time.After(2 * time.Second):
		t.Fatal("Login kept waiting for the refresh lock after its context ended")
	}
}

// TestLogin_RefreshWithoutExpiresIn verifies that a refresh response
// without expires_in is accepted and stored without an expiry, as the code
// exchange does.
func TestLogin_RefreshWithoutExpiresIn(t *testing.T) {
	resetDiscoveryCache()
	provider := newRefreshProvider(t, func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"refreshed-access-token","refresh_token":"rotated","token_type":"Bearer"}`
	})
	dir := t.TempDir()
	writeStoredToken(t, dir, provider.URL, expiredToken("still-good"))

	tok, prompt, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	assert.Equal(t, "refreshed-access-token", tok.AccessToken)
	assert.True(t, tok.Expiry.IsZero())
	stored := readStoredBundle(t, dir)
	assert.Equal(t, "refreshed-access-token", stored.AccessToken)
	assert.Nil(t, stored.ExpiresAt)
}

// TestLogin_RefreshInvalidGrantPeerWithoutRefreshTokenRunsFlow verifies
// that an expired peer token with no refresh token leads to the
// interactive flow without a second refresh request.
func TestLogin_RefreshInvalidGrantPeerWithoutRefreshTokenRunsFlow(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(form url.Values) (int, string) {
		if form.Get("grant_type") != "refresh_token" {
			return refreshed(form)
		}
		writeStoredTokenFromHandler(t, dir, provider.URL, expiredToken(""))
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := Login(ctx, "",
		WithIssuer(provider.URL),
		WithClientID("test-client"),
		withTokenDir(dir),
		WithKeyboardFlow(),
		withInput(strings.NewReader("fake-auth-code\n")),
		withOutput(&bytes.Buffer{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "login-access-token", tok.AccessToken)
	assert.Equal(t, []string{"refresh_token", "authorization_code"}, provider.grants())
}

// TestLogin_RefreshPeerRetryKeepsPeerRefreshToken verifies that when the
// retry with a peer's refresh token gets no new refresh token back, the
// file keeps the peer's refresh token. The provider has already rejected
// the original one.
func TestLogin_RefreshPeerRetryKeepsPeerRefreshToken(t *testing.T) {
	resetDiscoveryCache()
	dir := t.TempDir()
	var provider *refreshProvider
	provider = newRefreshProvider(t, func(form url.Values) (int, string) {
		if form.Get("refresh_token") == "peer-refresh-token" {
			return http.StatusOK, `{"access_token":"refreshed-access-token","token_type":"Bearer","expires_in":3600}`
		}
		writeStoredTokenFromHandler(t, dir, provider.URL, expiredToken("peer-refresh-token"))
		return http.StatusBadRequest, invalidGrantJSON
	})
	writeStoredToken(t, dir, provider.URL, expiredToken("already-used"))

	tok, _, err := loginNoPrompt(t, provider.URL, dir)
	require.NoError(t, err)
	assert.Equal(t, "peer-refresh-token", tok.RefreshToken)
	assert.Equal(t, "peer-refresh-token", readStoredBundle(t, dir).RefreshToken)
}

// TestRetryReplace verifies the replace retry schedule: transient errors
// are retried with delays doubling from 5 ms up to 50 ms until they add
// up to 250 ms, and any other error is returned at once.
func TestRetryReplace(t *testing.T) {
	errTransient := errors.New("transient")
	errOther := errors.New("other")
	transient := func(err error) bool { return errors.Is(err, errTransient) }

	t.Run("succeeds after transient failures", func(t *testing.T) {
		var slept []time.Duration
		calls := 0
		err := retryReplace(func() error {
			calls++
			if calls < 3 {
				return errTransient
			}
			return nil
		}, transient, func(d time.Duration) { slept = append(slept, d) })
		require.NoError(t, err)
		assert.Equal(t, 3, calls)
		assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, slept)
	})

	t.Run("returns other errors at once", func(t *testing.T) {
		var slept []time.Duration
		err := retryReplace(func() error { return errOther }, transient,
			func(d time.Duration) { slept = append(slept, d) })
		require.ErrorIs(t, err, errOther)
		assert.Empty(t, slept)
	})

	t.Run("gives up after 250 ms of delays", func(t *testing.T) {
		var slept []time.Duration
		calls := 0
		err := retryReplace(func() error { calls++; return errTransient }, transient,
			func(d time.Duration) { slept = append(slept, d) })
		require.ErrorIs(t, err, errTransient)
		ms := time.Millisecond
		assert.Equal(t, []time.Duration{5 * ms, 10 * ms, 20 * ms, 40 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms}, slept)
		assert.Equal(t, len(slept)+1, calls)
	})
}

// TestIsTransientReplaceError verifies that only access denied and sharing
// violation on Windows are retried.
func TestIsTransientReplaceError(t *testing.T) {
	link := func(errno syscall.Errno) error {
		return &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errno}
	}
	assert.True(t, isTransientReplaceError("windows", link(5)))
	assert.True(t, isTransientReplaceError("windows", link(32)))
	assert.False(t, isTransientReplaceError("windows", link(2)))
	assert.False(t, isTransientReplaceError("windows", errors.New("rename failed")))
	assert.False(t, isTransientReplaceError("linux", link(5)))
	assert.False(t, isTransientReplaceError("darwin", link(32)))
}
