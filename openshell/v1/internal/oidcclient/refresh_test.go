// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
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
		refresh  string // default "secret-refresh-token"
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
		"invalid expiry echoes the refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusOK, `{"access_token":"a","expires_in":123456789012345678901234567890}`)
			},
			refresh: "123456789012345678901234567890",
			want:    "invalid token response JSON",
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
			want: `: "invalid_client"`,
		},
		"proxy error page": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadGateway, "<html>502 Bad Gateway</html>")
			},
			want: "HTTP 502",
		},
		"long error code": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"`+strings.Repeat("x", 1000)+`"}`)
			},
			want: `: "` + strings.Repeat("x", maxErrorCode) + `..."`,
		},
		"error code that echoes the refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad secret-refresh-token"}`)
			},
			want: `: "bad [refresh token]"`,
		},
		"error code that echoes the form-encoded refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad secret%2Frefresh"}`)
			},
			refresh: "secret/refresh",
			want:    `: "bad [refresh token]"`,
		},
		"error code that echoes a refresh token with special characters": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad secret/refresh"}`)
			},
			refresh: "secret/refresh",
			want:    `: "bad [refresh token]"`,
		},
		"error code that echoes the refresh token with lowercase hex": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad rt%2fab%2bcd%3def"}`)
			},
			refresh: "rt/ab+cd=ef",
			want:    `: "[error code containing the refresh token]"`,
		},
		"error code that echoes the path-escaped refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad rt%2Fab+cd=ef"}`)
			},
			refresh: "rt/ab+cd=ef",
			want:    `: "[error code containing the refresh token]"`,
		},
		"error code that echoes the fully percent-encoded refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad %72%74%2F%61%62%2B%63%64%3D%65%66"}`)
			},
			refresh: "rt/ab+cd=ef",
			want:    `: "[error code containing the refresh token]"`,
		},
		"error code that echoes the twice-encoded refresh token": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad rt%252Fab%252Bcd%253Def"}`)
			},
			refresh: "rt/ab+cd=ef",
			want:    `: "[error code containing the refresh token]"`,
		},
		"error code that echoes the refresh token encoded four times": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad rt%2525252Fab"}`)
			},
			refresh: "rt/ab",
			want:    `: "[error code containing the refresh token]"`,
		},
		"error code with a malformed escape": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"bad%zz %2"}`)
			},
			want: `: "bad%zz %2"`,
		},
		"long error code cut inside a character": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusBadRequest, `{"error":"`+strings.Repeat("x", maxErrorCode-1)+`é"}`)
			},
			want: `: "` + strings.Repeat("x", maxErrorCode-1) + `..."`,
		},
		"error code with status 200": {
			endpoint: func(t *testing.T) string {
				return respondWith(t, http.StatusOK, `{"error":"temporarily_unavailable","access_token":"a"}`)
			},
			want: `: "temporarily_unavailable"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			refresh := tc.refresh
			if refresh == "" {
				refresh = "secret-refresh-token"
			}
			_, err := refreshToken(context.Background(), tc.endpoint(t), "test-client", refresh, nil)
			require.ErrorIs(t, err, ErrRefresh)
			assert.NotErrorIs(t, err, ErrInvalidGrant)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), refresh)
			assert.NotContains(t, err.Error(), url.QueryEscape(refresh))
			assert.False(t, holdsSecret(err.Error(), refresh), "refresh token recoverable from %q", err)
			assert.Less(t, len(err.Error()), 200)
		})
	}
}

// TestPercentDecode verifies that every hex digit in either case decodes,
// and that malformed or cut-off escapes stay as they are.
func TestPercentDecode(t *testing.T) {
	for in, want := range map[string]string{
		"%00%09%0a%0A":       "\x00\t\n\n",
		"%20%30%90%a0%A0%f0": " 0\x90\xa0\xa0\xf0",
		"%af%AF%Fa%fF":       "\xaf\xaf\xfa\xff",
		"rt%2Fab":            "rt/ab",
		"%g0%0g%%41":         "%g0%0g%A",
		"a%4":                "a%4",
		"%":                  "%",
	} {
		assert.Equal(t, want, percentDecode(in), "percentDecode(%q)", in)
	}
}

// TestRefreshToken_ExpiresIn verifies that an absent or null expires_in
// gives a token without an expiry, and that a present one sets it.
func TestRefreshToken_ExpiresIn(t *testing.T) {
	for name, tc := range map[string]struct {
		body     string
		noExpiry bool
	}{
		"absent": {body: `{"access_token":"a"}`, noExpiry: true},
		"null":   {body: `{"access_token":"a","expires_in":null}`, noExpiry: true},
		"set":    {body: `{"access_token":"a","expires_in":60}`},
		// Larger than time.Duration can hold; capped as golang.org/x/oauth2 does.
		"huge": {body: `{"access_token":"a","expires_in":10000000000}`},
	} {
		t.Run(name, func(t *testing.T) {
			tok, err := refreshToken(context.Background(), respondWith(t, http.StatusOK, tc.body), "test-client", "r", nil)
			require.NoError(t, err)
			assert.Equal(t, tc.noExpiry, tok.Expiry.IsZero())
			if !tc.noExpiry {
				assert.True(t, tok.Expiry.After(time.Now().Add(59*time.Second)), "expiry %v", tok.Expiry)
			}
			if name == "set" {
				assert.WithinDuration(t, time.Now().Add(time.Minute), tok.Expiry, 5*time.Second)
			}
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

// TestRefresh_FinishGrant verifies that with FinishGrant a token request
// already sent completes and is stored when ctx ends, and that without it
// the request is abandoned and the file keeps the old token.
func TestRefresh_FinishGrant(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(fmt.Sprintf("FinishGrant=%v", finish), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			issuer, _ := newRefreshServer(t, func() {
				// The provider has accepted the refresh when the caller
				// gives up.
				cancel()
				time.Sleep(50 * time.Millisecond)
			}, func() (int, string) {
				return http.StatusOK, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`
			})
			dir := t.TempDir()
			require.NoError(t, WriteToken(dir, oidctoken.FromToken(&oauth2.Token{
				AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
			}, issuer, "client")))

			res, err := Refresh(ctx, Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow, FinishGrant: finish})
			stored, readErr := ReadToken(dir)
			require.NoError(t, readErr)
			entries, dirErr := os.ReadDir(dir)
			require.NoError(t, dirErr)
			assert.Len(t, entries, 1, "only %s is left in the directory", oidctoken.FileName)
			if finish {
				require.NoError(t, err)
				assert.True(t, res.Granted)
				assert.Equal(t, "new-access", res.Token.AccessToken)
				assert.Equal(t, "rotated", stored.RefreshToken)
				return
			}
			require.ErrorIs(t, err, ErrRefresh)
			assert.Contains(t, err.Error(), "context canceled")
			assert.Equal(t, "old-refresh", stored.RefreshToken)
		})
	}
}

func TestScopesWithOpenID(t *testing.T) {
	assert.Equal(t, []string{"openid"}, ScopesWithOpenID(nil))
	assert.Equal(t, []string{"openid", "a", "b"}, ScopesWithOpenID([]string{"a", "openid", "b", "openid"}))
	assert.Equal(t, []string{"openid", "a"}, ScopesWithOpenID([]string{"openid", "a"}))
}
