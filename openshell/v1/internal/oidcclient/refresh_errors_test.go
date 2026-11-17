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

			tok, err := Refresh(context.Background(), Request{Dir: dir, Issuer: issuer, ClientID: "client", Window: RefreshWindow})
			require.Error(t, err)
			assert.Nil(t, tok)
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
