// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// respondWith starts a token endpoint that answers every request with
// status and body, and returns its URL.
func respondWith(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/token"
}

// TestRefreshToken_Errors verifies that every failure of the refresh
// request is reported as ErrRefresh, is not mistaken for
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
				return respondWith(t, http.StatusOK, "not json")
			},
			want: "invalid token response JSON",
		},
		"server error without error code": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusInternalServerError, `{"access_token":"a"}`)
			},
			want: "HTTP 500",
		},
		"error code with status 401": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusUnauthorized, `{"error":"invalid_client"}`)
			},
			want: ": invalid_client",
		},
		"error code with status 200": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusOK, `{"error":"temporarily_unavailable","access_token":"a"}`)
			},
			want: ": temporarily_unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := refreshToken(context.Background(), tc.endpoint(t), "test-client", "secret-refresh-token", nil)
			require.ErrorIs(t, err, ErrRefresh)
			assert.NotErrorIs(t, err, ErrInvalidGrant)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "secret-refresh-token")
		})
	}
}

// TestFresh verifies the freshness test at the window boundary. With the
// 10 second RefreshWindow it is oauth2.Token.Valid: a token that expires
// exactly one window from now is still fresh, one nanosecond less is not.
func TestFresh(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now = func() time.Time { return base }
	t.Cleanup(func() { now = time.Now })

	at := func(d time.Duration) *oauth2.Token {
		return &oauth2.Token{AccessToken: "a", Expiry: base.Add(d)}
	}
	w := RefreshWindow
	assert.True(t, Fresh(at(w), w), "expires exactly one window from now")
	assert.False(t, Fresh(at(w-time.Nanosecond), w), "expires just inside the window")
	assert.True(t, Fresh(at(time.Hour), w))
	assert.False(t, Fresh(at(-time.Second), w), "already expired")
	assert.True(t, Fresh(&oauth2.Token{AccessToken: "a"}, w), "no expiry")
	assert.False(t, Fresh(&oauth2.Token{Expiry: base.Add(time.Hour)}, w), "no access token")
	assert.False(t, Fresh(nil, w))
	assert.True(t, Fresh(at(30*time.Second), 30*time.Second))
	assert.False(t, Fresh(at(30*time.Second-time.Nanosecond), 30*time.Second))
}

// TestFreshMatchesOAuth2Valid verifies that with RefreshWindow, Fresh
// agrees with oauth2.Token.Valid, which Login used before the refresh code
// moved here. The expiries stay a second away from the boundary so the
// two clocks cannot disagree.
func TestFreshMatchesOAuth2Valid(t *testing.T) {
	for _, d := range []time.Duration{-time.Hour, -time.Second, 0, 5 * time.Second, 9 * time.Second, 11 * time.Second, time.Hour} {
		tok := &oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(d)}
		assert.Equal(t, tok.Valid(), Fresh(tok, RefreshWindow), "expiry in %v", d)
	}
	for _, tok := range []*oauth2.Token{{AccessToken: "a"}, {}, {Expiry: time.Now().Add(time.Hour)}} {
		assert.Equal(t, tok.Valid(), Fresh(tok, RefreshWindow), "%+v", tok)
	}
}
