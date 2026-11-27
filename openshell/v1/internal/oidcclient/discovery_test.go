// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiscover_Errors covers the discovery failures that the oidc package
// tests do not reach. Each is reported as ErrDiscovery, which is what
// Login stops on and what the gateway reports.
func TestDiscover_Errors(t *testing.T) {
	serve := func(t *testing.T, handler func(srv *httptest.Server, w http.ResponseWriter)) string {
		t.Helper()
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { handler(srv, w) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	doc := func(srv *httptest.Server, authorize, token string) string {
		return fmt.Sprintf(`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q}`, srv.URL, authorize, token)
	}

	for name, tc := range map[string]struct {
		issuer func(t *testing.T) string
		want   string
	}{
		"plain HTTP to a remote issuer": {
			issuer: func(*testing.T) string { return "http://idp.example.com" },
			want:   "issuer URL must use HTTPS",
		},
		"issuer without a host": {
			issuer: func(*testing.T) string { return "https://" },
			want:   "invalid issuer URL",
		},
		"provider not listening": {
			issuer: func(*testing.T) string {
				srv := httptest.NewServer(http.NotFoundHandler())
				srv.Close()
				return srv.URL
			},
			want: "dial tcp",
		},
		"truncated response": {
			issuer: func(t *testing.T) string {
				return serve(t, func(_ *httptest.Server, w http.ResponseWriter) {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte(`{"issuer":`))
				})
			},
			want: "failed to read discovery response",
		},
		"missing authorization_endpoint": {
			issuer: func(t *testing.T) string {
				return serve(t, func(srv *httptest.Server, w http.ResponseWriter) {
					_, _ = w.Write([]byte(doc(srv, "", srv.URL+"/token")))
				})
			},
			want: "missing authorization_endpoint",
		},
		"token endpoint that is not a URL": {
			issuer: func(t *testing.T) string {
				return serve(t, func(srv *httptest.Server, w http.ResponseWriter) {
					_, _ = w.Write([]byte(doc(srv, srv.URL+"/authorize", "/token")))
				})
			},
			want: "invalid token_endpoint URL",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ResetDiscoveryCache()
			_, err := Discover(context.Background(), tc.issuer(t))
			require.ErrorIs(t, err, ErrDiscovery)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
