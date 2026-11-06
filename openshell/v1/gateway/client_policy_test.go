// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type parityServer struct {
	pb.UnimplementedOpenShellServer
	health func(context.Context) (*pb.HealthResponse, error)
}

func (s *parityServer) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return s.health(ctx)
}

type parityLogger struct {
	mu          sync.Mutex
	completions int
}

func (l *parityLogger) Debug(message string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if message == "RPC completed" {
		l.completions++
	}
}
func (l *parityLogger) Info(string, ...any)                          {}
func (l *parityLogger) Error(_ error, message string, fields ...any) { l.Debug(message, fields...) }

func TestClientPolicy_ConstructorParity(t *testing.T) {
	for _, viaGateway := range []bool{false, true} {
		t.Run(fmt.Sprint(viaGateway), func(t *testing.T) {
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer()
			handler := &parityServer{}
			pb.RegisterOpenShellServer(server, handler)
			go func() { _ = server.Serve(lis) }()
			defer server.Stop()
			address := "http://" + lis.Addr().String()
			setupGateway(t, "policy", fmt.Sprintf(`{"gateway_endpoint":%q,"auth_mode":"none"}`, address))
			construct := func(cfg types.Config) (*v1.Client, error) {
				cfg.Address = address
				cfg.Auth = v1.NoAuth()
				if viaGateway {
					return NewClient("policy", WithAuth(v1.NoAuth()), WithTimeout(cfg.Timeout), WithRetryPolicy(cfg.RetryPolicy), WithLogger(cfg.Logger))
				}
				return v1.NewClient(cfg)
			}
			for _, cfg := range []types.Config{
				{Timeout: -1}, {RetryPolicy: &types.RetryPolicy{MaxRetries: -1}},
				{RetryPolicy: &types.RetryPolicy{InitialWait: -1}}, {RetryPolicy: &types.RetryPolicy{MaxWait: -1}},
				{RetryPolicy: &types.RetryPolicy{InitialWait: time.Second, MaxWait: time.Millisecond}},
				{RetryPolicy: &types.RetryPolicy{MaxWait: time.Millisecond}}, {RetryPolicy: &types.RetryPolicy{InitialWait: 6 * time.Second}},
			} {
				c, err := construct(cfg)
				if c != nil {
					_ = c.Close()
				}
				assert.True(t, types.IsInvalidArgument(err), "%+v: %v", cfg, err)
			}
			handler.health = func(ctx context.Context) (*pb.HealthResponse, error) {
				select {
				case <-time.After(100 * time.Millisecond):
					return &pb.HealthResponse{}, nil
				case <-ctx.Done():
					return nil, status.FromContextError(ctx.Err()).Err()
				}
			}
			c, err := construct(types.Config{Timeout: 30 * time.Millisecond})
			require.NoError(t, err)
			_, err = c.Health().Check(context.Background())
			assert.True(t, types.IsDeadlineExceeded(err))
			for _, tc := range []struct {
				deadline time.Duration
				expired  bool
			}{{10 * time.Millisecond, true}, {time.Second, false}} {
				ctx, cancel := context.WithTimeout(context.Background(), tc.deadline)
				_, err = c.Health().Check(ctx)
				cancel()
				assert.Equal(t, tc.expired, types.IsDeadlineExceeded(err))
			}
			require.NoError(t, c.Close())
			// No timeout and nil retry/logger retain ordinary unary behavior.
			c, err = construct(types.Config{})
			require.NoError(t, err)
			_, err = c.Health().Check(context.Background())
			require.NoError(t, err)
			require.NoError(t, c.Close())
			var attempts atomic.Int32
			handler.health = func(context.Context) (*pb.HealthResponse, error) {
				if attempts.Add(1) == 1 {
					return nil, status.Error(codes.Unavailable, "transient")
				}
				return &pb.HealthResponse{}, nil
			}
			c, err = construct(types.Config{RetryPolicy: &types.RetryPolicy{MaxRetries: 1, InitialWait: time.Millisecond}})
			require.NoError(t, err)
			_, err = c.Health().Check(context.Background())
			require.NoError(t, err)
			assert.Equal(t, int32(2), attempts.Load())
			require.NoError(t, c.Close())
			handler.health = func(context.Context) (*pb.HealthResponse, error) {
				attempts.Add(1)
				return nil, status.Error(codes.Unavailable, "transient")
			}
			attempts.Store(0)
			c, err = construct(types.Config{RetryPolicy: &types.RetryPolicy{}})
			require.NoError(t, err)
			_, err = c.Health().Check(context.Background())
			assert.True(t, types.IsUnavailable(err))
			assert.Equal(t, int32(1), attempts.Load())
			require.NoError(t, c.Close())
			logger := &parityLogger{}
			handler.health = func(context.Context) (*pb.HealthResponse, error) { return &pb.HealthResponse{}, nil }
			c, err = construct(types.Config{Logger: logger})
			require.NoError(t, err)
			_, err = c.Health().Check(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 1, logger.completions)
			require.NoError(t, c.Close())
		})
	}
}
