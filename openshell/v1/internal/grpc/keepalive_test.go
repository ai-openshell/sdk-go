// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// blackholeProxy forwards TCP traffic until stall is called, then silently
// drops everything while keeping both sockets open, like a half-open
// connection behind a NAT or load balancer.
type blackholeProxy struct {
	lis     net.Listener
	backend string
	stalled atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func newBlackholeProxy(t *testing.T, backend string) *blackholeProxy {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &blackholeProxy{lis: lis, backend: backend}
	t.Cleanup(p.close)
	go p.serve()
	return p
}

func (p *blackholeProxy) serve() {
	for {
		client, err := p.lis.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.backend)
		if err != nil {
			_ = client.Close()
			return
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go p.pipe(server, client)
		go p.pipe(client, server)
	}
}

func (p *blackholeProxy) pipe(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		if p.stalled.Load() {
			continue
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

func (p *blackholeProxy) stall() { p.stalled.Store(true) }

func (p *blackholeProxy) close() {
	_ = p.lis.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
}

func TestKeepaliveSurfacesHalfOpenConnectionAsUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a keepalive timeout")
	}

	// grpc-go raises any ping interval below 10s to 10s, so this is the
	// fastest the client can notice.
	orig := keepaliveParams
	keepaliveParams = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: time.Second}
	t.Cleanup(func() { keepaliveParams = orig })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: time.Second}))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	proxy := newBlackholeProxy(t, lis.Addr().String())

	conn, err := NewConnection("http://"+proxy.lis.Addr().String(), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	proxy.stall()
	start := time.Now()
	_, err = stream.Recv()
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err), "got %v", err)
	require.NoError(t, ctx.Err())
	require.Less(t, time.Since(start), 20*time.Second)
}
