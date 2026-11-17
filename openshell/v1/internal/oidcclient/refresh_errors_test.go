// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// newRefreshServer starts a provider whose token endpoint calls onToken
// before answering with respond, and returns its URL and a counter of
// token requests.
func newRefreshServer(t *testing.T, onToken func(), respond func() (int, string)) (string, *atomic.Int32) {
	t.Helper()
	ResetDiscoveryCache()
	var calls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/.well-known/openid-configuration" {
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q}`,
				srv.URL, srv.URL+"/authorize", srv.URL+"/token")
			return
		}
		calls.Add(1)
		onToken()
		status, body := respond()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// TestRefresh_NotRefreshable verifies which failures Refresh reports as
// ErrNotRefreshable: those where the file under the lock gives it nothing
// to refresh with, before the grant or after invalid_grant. A failed retry
// with a peer's refresh token is a refresh error, not one of these.
func TestRefresh_NotRefreshable(t *testing.T) {
	expired := func(issuer, clientID, refresh string) oidctoken.Bundle {
		return oidctoken.FromToken(&oauth2.Token{
			AccessToken: "old-access", RefreshToken: refresh, Expiry: time.Now().Add(-time.Minute),
		}, issuer, clientID)
	}
	invalidGrant := func() (int, string) { return http.StatusBadRequest, `{"error":"invalid_grant"}` }

	for name, tc := range map[string]struct {
		file           func(t *testing.T, dir, issuer string) // the file under the lock
		peer           func(dir, issuer string)               // what a peer leaves before the provider answers
		want           string
		calls          int32
		notRefreshable bool
	}{
		"no file": {
			file:           func(*testing.T, string, string) {},
			want:           "does not exist",
			notRefreshable: true,
		},
		"unreadable file": {
			file: func(t *testing.T, dir, _ string) {
				require.NoError(t, os.WriteFile(filepath.Join(dir, oidctoken.FileName), []byte("{"), 0o600))
			},
			want:           "invalid JSON",
			notRefreshable: true,
		},
		"another client": {
			file: func(t *testing.T, dir, issuer string) {
				require.NoError(t, WriteToken(dir, expired(issuer, "other-client", "old-refresh")))
			},
			want:           "another issuer or client",
			notRefreshable: true,
		},
		"another issuer": {
			file: func(t *testing.T, dir, _ string) {
				require.NoError(t, WriteToken(dir, expired("https://other.example", "client", "old-refresh")))
			},
			want:           "another issuer or client",
			notRefreshable: true,
		},
		"no refresh token": {
			file: func(t *testing.T, dir, issuer string) {
				require.NoError(t, WriteToken(dir, expired(issuer, "client", "")))
			},
			want:           "no refresh token",
			notRefreshable: true,
		},
		"invalid_grant, peer removed the file": {
			peer: func(dir, _ string) {
				if err := os.Remove(filepath.Join(dir, oidctoken.FileName)); err != nil {
					t.Errorf("remove: %v", err)
				}
			},
			want: "invalid_grant", calls: 1, notRefreshable: true,
		},
		"invalid_grant, peer token for another client": {
			peer: func(dir, issuer string) {
				if err := WriteToken(dir, expired(issuer, "other-client", "peer-refresh")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			want: "invalid_grant", calls: 1, notRefreshable: true,
		},
		"invalid_grant, same refresh token": {
			peer: func(string, string) {},
			want: "invalid_grant", calls: 1, notRefreshable: true,
		},
		"invalid_grant, peer without refresh token": {
			peer: func(dir, issuer string) {
				if err := WriteToken(dir, expired(issuer, "client", "")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			want: "invalid_grant", calls: 1, notRefreshable: true,
		},
		"invalid_grant again with the peer's refresh token": {
			peer: func(dir, issuer string) {
				if err := WriteToken(dir, expired(issuer, "client", "peer-refresh")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			want: "invalid_grant", calls: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var issuer string
			peered := false
			issuer, calls := newRefreshServer(t, func() {
				if tc.peer != nil && !peered {
					peered = true
					tc.peer(dir, issuer)
				}
			}, invalidGrant)
			if tc.file != nil {
				tc.file(t, dir, issuer)
			} else {
				require.NoError(t, WriteToken(dir, expired(issuer, "client", "old-refresh")))
			}

			res, err := Refresh(context.Background(), Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow})
			require.Error(t, err)
			assert.Equal(t, Result{}, res)
			assert.Contains(t, err.Error(), tc.want)
			assert.Equal(t, tc.notRefreshable, errors.Is(err, ErrNotRefreshable), "ErrNotRefreshable in %v", err)
			assert.NotErrorIs(t, err, ErrTokenPersist)
			if tc.calls > 0 {
				require.ErrorIs(t, err, ErrInvalidGrant)
			}
			assert.Equal(t, tc.calls, calls.Load())
		})
	}
}

// TestRefresh_FileChangedDuringGrant verifies that Refresh does not write
// the new token over a file that changed while the token request was in
// flight: removed by a logout, or replaced by a login elsewhere. A file it
// cannot read at that point is written over.
func TestRefresh_FileChangedDuringGrant(t *testing.T) {
	expired := func(issuer, refresh string) oidctoken.Bundle {
		return oidctoken.FromToken(&oauth2.Token{
			AccessToken: "old-access", RefreshToken: refresh, Expiry: time.Now().Add(-time.Minute),
		}, issuer, "client")
	}
	rotated := func() (int, string) {
		return http.StatusOK, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`
	}

	for name, tc := range map[string]struct {
		change    func(t *testing.T, dir, issuer string)
		wantToken string // empty: ErrNotRefreshable
		wantFile  string // refresh token left in the file; empty: no file
		granted   bool
		unix      bool // needs file modes that keep the owner out
	}{
		"logout removed the file": {
			change: func(t *testing.T, dir, _ string) {
				if err := os.Remove(filepath.Join(dir, oidctoken.FileName)); err != nil {
					t.Errorf("remove: %v", err)
				}
			},
		},
		"a login wrote a fresh token": {
			change: func(t *testing.T, dir, issuer string) {
				if err := WriteToken(dir, oidctoken.FromToken(&oauth2.Token{
					AccessToken: "login-access", RefreshToken: "login-refresh", Expiry: time.Now().Add(time.Hour),
				}, issuer, "client")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			wantToken: "login-access",
			wantFile:  "login-refresh",
		},
		"another refresh token, expired": {
			change: func(t *testing.T, dir, issuer string) {
				if err := WriteToken(dir, expired(issuer, "other-refresh")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			wantFile: "other-refresh",
		},
		"unreadable while the request runs": {
			// For example the CLI rewriting the file in place. The new
			// token is the only one known to work, so it is written.
			unix: true,
			change: func(t *testing.T, dir, _ string) {
				if err := os.Chmod(filepath.Join(dir, oidctoken.FileName), 0o000); err != nil {
					t.Errorf("chmod: %v", err)
				}
			},
			wantToken: "new-access",
			wantFile:  "rotated",
			granted:   true,
		},
		"a token for another client with the same refresh token": {
			change: func(t *testing.T, dir, issuer string) {
				if err := WriteToken(dir, oidctoken.FromToken(&oauth2.Token{
					AccessToken: "other-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
				}, issuer, "other-client")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			wantFile: "old-refresh",
		},
		"a token for another client": {
			change: func(t *testing.T, dir, issuer string) {
				if err := WriteToken(dir, oidctoken.FromToken(&oauth2.Token{
					AccessToken: "other-access", RefreshToken: "other-refresh", Expiry: time.Now().Add(time.Hour),
				}, issuer, "other-client")); err != nil {
					t.Errorf("write: %v", err)
				}
			},
			wantFile: "other-refresh",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.unix && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("needs a file the process cannot read")
			}
			dir := t.TempDir()
			var issuer string
			issuer, calls := newRefreshServer(t, func() { tc.change(t, dir, issuer) }, rotated)
			require.NoError(t, WriteToken(dir, expired(issuer, "old-refresh")))

			res, err := Refresh(context.Background(), Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow})
			assert.Equal(t, int32(1), calls.Load())
			assert.Equal(t, tc.granted, res.Granted)
			if tc.wantToken == "" {
				require.ErrorIs(t, err, ErrNotRefreshable)
				assert.Contains(t, err.Error(), "changed during the refresh")
				assert.Nil(t, res.Token)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantToken, res.Token.AccessToken)
			}
			got, readErr := ReadToken(dir)
			require.NoError(t, readErr)
			if tc.wantFile == "" {
				assert.Nil(t, got, "the removed file came back")
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tc.wantFile, got.RefreshToken)
			}
		})
	}
}

// TestRefresh_WriteFailure verifies that a directory Refresh cannot write
// to is reported before the token request, so the refresh token is not
// spent, and that a write failing after the request still returns the
// new token.
func TestRefresh_WriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	stored := func(issuer string) oidctoken.Bundle {
		return oidctoken.FromToken(&oauth2.Token{
			AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
		}, issuer, "client")
	}
	rotated := func() (int, string) {
		return http.StatusOK, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`
	}

	t.Run("before the token request", func(t *testing.T) {
		dir := t.TempDir()
		issuer, calls := newRefreshServer(t, func() {}, rotated)
		require.NoError(t, WriteToken(dir, stored(issuer)))
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		res, err := Refresh(context.Background(), Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow})
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.Equal(t, Result{}, res)
		assert.Equal(t, int32(0), calls.Load(), "the refresh token was spent")
	})
	t.Run("after the token request", func(t *testing.T) {
		dir := t.TempDir()
		issuer, calls := newRefreshServer(t, func() {
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Errorf("chmod: %v", err)
			}
		}, rotated)
		require.NoError(t, WriteToken(dir, stored(issuer)))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		res, err := Refresh(context.Background(), Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow})
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.True(t, res.Granted)
		require.NotNil(t, res.Token)
		assert.Equal(t, "new-access", res.Token.AccessToken)
		assert.Equal(t, "rotated", res.Token.RefreshToken)
		assert.Equal(t, int32(1), calls.Load())
		got, readErr := ReadToken(dir)
		require.NoError(t, readErr)
		assert.Equal(t, "old-refresh", got.RefreshToken)
	})
}

// TestRefresh_LockWaitError verifies that a lock wait cut off by ctx says
// what it was waiting for and wraps ctx's error.
func TestRefresh_LockWaitError(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockDir(context.Background(), dir)
	require.NoError(t, err)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = Refresh(ctx, Request{Dir: dir, Issuer: "https://idp.example.com", ClientID: "client", Window: RefreshWindow})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "waiting for another refresh of "+oidctoken.FileName)
}
