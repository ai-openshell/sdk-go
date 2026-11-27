// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// authRecorder is a TLS gRPC server that records the authorization header
// of every call and answers each with Unimplemented. It listens on TCP
// rather than bufconn because NewClient dials the endpoint in
// metadata.json and takes no dialer.
type authRecorder struct {
	addr string
	mu   sync.Mutex
	seen []string
}

func newAuthRecorder(t *testing.T) *authRecorder {
	t.Helper()
	// Borrow httptest's self-signed certificate for the gRPC listener.
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certSrv.TLS.Certificates[0]
	certSrv.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	r := &authRecorder{addr: lis.Addr().String()}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewServerTLSFromCert(&cert)),
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			md, _ := metadata.FromIncomingContext(stream.Context())
			r.mu.Lock()
			r.seen = append(r.seen, md.Get("authorization")...)
			r.mu.Unlock()
			return status.Error(codes.Unimplemented, "recorded")
		}),
	)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return r
}

func (r *authRecorder) headers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// settle lets a held-up provider answer and waits until the renewal has
// written the file, so no renewal touches the directory after the test.
func settle(t *testing.T, answer func(), dir string) {
	t.Helper()
	answer()
	require.Eventually(t, func() bool {
		return readBundle(t, dir).RefreshToken == "rotated-refresh"
	}, 10*time.Second, 10*time.Millisecond)
}

// newOIDCGatewayClient registers an OIDC gateway "rpc-gw" at addr whose
// metadata names issuer, stores access with a refresh token and the given
// expiry relative to now, and returns a client from NewClient with its
// gateway directory.
func newOIDCGatewayClient(t *testing.T, addr, issuer, access string, expiresIn time.Duration) (*v1.Client, string) {
	t.Helper()
	tmp := setupGateway(t, "rpc-gw", fmt.Sprintf(
		`{"gateway_endpoint":%q,"auth_mode":"oidc","oidc_issuer":%q,"oidc_client_id":"openshell-cli"}`, addr, issuer))
	dir := filepath.Join(tmp, "openshell", "gateways", "rpc-gw")
	storeToken(t, dir, issuer, access, "stored-refresh", expiresIn)

	client, err := NewClient("rpc-gw", WithTLS(&types.TLSConfig{Insecure: true}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client, dir
}

// TestNewClient_OIDCRenewsExpiredToken checks the issue's acceptance
// criterion end to end: a client from NewClient, holding only an expired
// token and a refresh token, renews the token and sends the new one.
func TestNewClient_OIDCRenewsExpiredToken(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, rotating)
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "expired-access", -time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)

	assert.Equal(t, []string{"Bearer refreshed-access"}, rec.headers())
	assert.Len(t, p.requests(), 1)
	assert.Equal(t, "rotated-refresh", readBundle(t, dir).RefreshToken)
}

func TestNewClient_OIDCCachesShortLivedToken(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, func(url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"short-access","refresh_token":"rotated-refresh","expires_in":20}`
	})
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "expired-access", -time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err))
	storeToken(t, dir, p.URL, "peer-access", "peer-refresh", time.Hour)
	_, err = client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err))

	assert.Equal(t, []string{"Bearer short-access", "Bearer short-access"}, rec.headers())
	assert.Len(t, p.requests(), 1)
}

// TestNewClient_OIDCRenewalFailureIsUnauthenticated checks that a failed
// renewal reaches the caller as Unauthenticated, naming the provider's
// error and the login command, and that no call goes out.
func TestNewClient_OIDCRenewalFailureIsUnauthenticated(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, func(url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	})
	client, _ := newOIDCGatewayClient(t, rec.addr, p.URL, "expired-access", -time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Error(t, err)
	assert.True(t, v1.IsUnauthenticated(err), "got %v", err)
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.Contains(t, err.Error(), "run `openshell gateway login rpc-gw`")
	assert.NotContains(t, err.Error(), "stored-refresh")
	assert.Empty(t, rec.headers())
}

// TestNewClient_OIDCUsesCachedTokenOutsideWindow checks that the client
// keeps sending its cached token until it is within the 30 s refresh
// window: a token with 45 s left is neither refreshed nor read again.
func TestNewClient_OIDCUsesCachedTokenOutsideWindow(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, rotating)
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "first-access", 45*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)

	// A token written now is not picked up until the cached one is due.
	storeToken(t, dir, p.URL, "second-access", "stored-refresh", time.Hour)
	_, err = client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)

	assert.Equal(t, []string{"Bearer first-access", "Bearer first-access"}, rec.headers())
	assert.Empty(t, p.requests())
}

// TestNewClient_OIDCRenewsInBackground checks that a client holding a
// token inside the refresh window keeps sending it while a slow renewal
// runs in the background, and sends the renewed token once it is stored.
func TestNewClient_OIDCRenewsInBackground(t *testing.T) {
	rec := newAuthRecorder(t)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	// Released on every path, or closing the provider waits forever.
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	setRefreshWait(t, 100*time.Millisecond)
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "expiring-access", 20*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)
	assert.Equal(t, []string{"Bearer expiring-access"}, rec.headers(), "the call waited for the slow renewal")

	answer()
	require.Eventually(t, func() bool {
		_, err := client.Health().Check(ctx)
		h := rec.headers()
		return status.Code(err) == codes.Unimplemented && h[len(h)-1] == "Bearer refreshed-access"
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "rotated-refresh", readBundle(t, dir).RefreshToken)
	assert.Len(t, p.requests(), 1)
}

// TestNewClient_OIDCLogsFailureWithCachedToken checks that the logger
// given to NewClient hears about a token failure the client covers with
// its cached token: here the token file is removed while the cached token
// is inside the refresh window.
func TestNewClient_OIDCLogsFailureWithCachedToken(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, rotating)
	tmp := setupGateway(t, "rpc-gw", fmt.Sprintf(
		`{"gateway_endpoint":%q,"auth_mode":"oidc","oidc_issuer":%q,"oidc_client_id":"openshell-cli"}`, rec.addr, p.URL))
	dir := filepath.Join(tmp, "openshell", "gateways", "rpc-gw")
	// No refresh token, so nothing renews it.
	storeToken(t, dir, p.URL, "cached-access", "", 20*time.Second)
	logger := &recordingLogger{}
	client, err := NewClient("rpc-gw", WithTLS(&types.TLSConfig{Insecure: true}), WithLogger(logger))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)

	require.NoError(t, os.Remove(filepath.Join(dir, oidctoken.FileName)))
	_, err = client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)

	assert.Equal(t, []string{"Bearer cached-access", "Bearer cached-access"}, rec.headers())
	errs := logger.errors()
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], ErrTokenLoad)
}

// TestNewClient_OIDCLogsFailedRenewal checks that a renewal that fails
// while the token is still usable leaves the call working and reaches the
// logger given to NewClient.
func TestNewClient_OIDCLogsFailedRenewal(t *testing.T) {
	rec := newAuthRecorder(t)
	p := newTokenProvider(t, unavailable)
	tmp := setupGateway(t, "rpc-gw", fmt.Sprintf(
		`{"gateway_endpoint":%q,"auth_mode":"oidc","oidc_issuer":%q,"oidc_client_id":"openshell-cli"}`, rec.addr, p.URL))
	dir := filepath.Join(tmp, "openshell", "gateways", "rpc-gw")
	storeToken(t, dir, p.URL, "usable-access", "stored-refresh", 20*time.Second)
	logger := &recordingLogger{}
	client, err := NewClient("rpc-gw", WithTLS(&types.TLSConfig{Insecure: true}), WithLogger(logger))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)
	assert.Equal(t, []string{"Bearer usable-access"}, rec.headers())

	require.Eventually(t, func() bool { return len(logger.errors()) > 0 }, 5*time.Second, 10*time.Millisecond)
	errs := logger.errors()
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], ErrTokenLoad)
	assert.Contains(t, errs[0].Error(), "HTTP 503")
}

// TestNewClient_OIDCHungRenewalDelaysOneCall checks that while a renewal
// hangs, the first call waits refreshWait and later calls are not held up.
func TestNewClient_OIDCHungRenewalDelaysOneCall(t *testing.T) {
	rec := newAuthRecorder(t)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	setRefreshWait(t, time.Second)
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "usable-access", 25*time.Second)

	for i := range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		_, err := client.Health().Check(ctx)
		cancel()
		require.Equal(t, codes.Unimplemented, status.Code(err), "call %d reached the server: %v", i, err)
		if i > 0 {
			assert.Less(t, time.Since(start), 500*time.Millisecond, "call %d waited for the hung renewal", i)
		}
	}
	assert.Equal(t, []string{"Bearer usable-access", "Bearer usable-access", "Bearer usable-access", "Bearer usable-access"}, rec.headers())
	settle(t, answer, dir)
}

// TestNewClient_OIDCShortDeadlineUsesCachedToken checks that a call with
// a deadline shorter than refreshWait, made while a renewal hangs, goes out
// with the client's cached token that has not expired.
func TestNewClient_OIDCShortDeadlineUsesCachedToken(t *testing.T) {
	rec := newAuthRecorder(t)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	setRefreshWait(t, 2*time.Second)
	// Outside the 30 s window, so the first call caches it. The file keeps
	// whole seconds, so this leaves 4 to 5 s.
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "cached-access", 35*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)
	require.Empty(t, p.requests())

	// Now inside the window: the next call starts a renewal that hangs.
	time.Sleep(5500 * time.Millisecond)
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	_, err = client.Health().Check(short)
	require.Equal(t, codes.Unimplemented, status.Code(err), "the call reached the server: %v", err)
	assert.Equal(t, []string{"Bearer cached-access", "Bearer cached-access"}, rec.headers())
	settle(t, answer, dir)
}

// TestNewClient_OIDCFirstCallDeadlineIsNotUnauthenticated checks that the
// first call of a client, which has no cached token yet, fails with its
// own deadline, not as Unauthenticated, when the deadline ends while it
// waits for a slow renewal.
func TestNewClient_OIDCFirstCallDeadlineIsNotUnauthenticated(t *testing.T) {
	rec := newAuthRecorder(t)
	release := make(chan struct{})
	p := newTokenProvider(t, func(form url.Values) (int, string) {
		<-release
		return rotating(form)
	})
	answer := sync.OnceFunc(func() { close(release) })
	defer answer()
	setRefreshWait(t, 2*time.Second)
	client, dir := newOIDCGatewayClient(t, rec.addr, p.URL, "usable-access", 20*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := client.Health().Check(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err), "got %v", err)
	assert.False(t, v1.IsUnauthenticated(err), "got %v", err)
	assert.Empty(t, rec.headers())
	settle(t, answer, dir)
}
