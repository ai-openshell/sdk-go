// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	dm "github.com/ai-openshell/sdk-go/proto/datamodelv1"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type policyServer struct {
	pb.UnimplementedOpenShellServer
	health        func(context.Context) (*pb.HealthResponse, error)
	streamCalls   atomic.Int32
	streamStarted chan struct{}
	// failExec makes ExecSandbox fail with Unavailable before sending anything.
	failExec bool
}

func (s *policyServer) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return s.health(ctx)
}
func (s *policyServer) GetSandbox(context.Context, *pb.GetSandboxRequest) (*pb.SandboxResponse, error) {
	return &pb.SandboxResponse{Sandbox: &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb", Id: "sb"}, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_PROVISIONING}}}, nil
}
func (s *policyServer) WatchSandbox(_ *pb.WatchSandboxRequest, stream grpc.ServerStreamingServer[pb.SandboxStreamEvent]) error {
	s.streamCalls.Add(1)
	if err := stream.Send(&pb.SandboxStreamEvent{Payload: &pb.SandboxStreamEvent_Sandbox{Sandbox: &pb.Sandbox{Metadata: &dm.ObjectMeta{Name: "sb"}}}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return status.Error(codes.Unavailable, "stream failed")
}
func (s *policyServer) ExecSandbox(_ *pb.ExecSandboxRequest, stream grpc.ServerStreamingServer[pb.ExecSandboxEvent]) error {
	s.streamCalls.Add(1)
	if s.failExec {
		return status.Error(codes.Unavailable, "stream failed")
	}
	close(s.streamStarted)
	<-stream.Context().Done()
	return status.Error(codes.Unavailable, "stream failed")
}
func policyAddress(t *testing.T, s *policyServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	pb.RegisterOpenShellServer(server, s)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return "http://" + lis.Addr().String()
}
func newPolicyTestClient(t *testing.T, s *policyServer, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(Config{Address: policyAddress(t, s), Timeout: timeout})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}
func TestClientPolicy_Timeout(t *testing.T) {
	s := &policyServer{health: func(ctx context.Context) (*pb.HealthResponse, error) {
		select {
		case <-time.After(200 * time.Millisecond):
			return &pb.HealthResponse{}, nil
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}}
	c := newPolicyTestClient(t, s, 40*time.Millisecond)
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	_, err := c.Health().Check(caller)
	assert.True(t, IsDeadlineExceeded(err), "%v", err)
	assert.Less(t, time.Since(start), time.Second)
	assert.NoError(t, caller.Err())
	for _, tc := range []struct {
		duration time.Duration
		deadline bool
	}{{10 * time.Millisecond, true}, {time.Second, false}} {
		ctx, stop := context.WithTimeout(context.Background(), tc.duration)
		_, err = c.Health().Check(ctx)
		stop()
		assert.Equal(t, tc.deadline, IsDeadlineExceeded(err))
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Health().Check(ctx); done <- err }()
	stop()
	assert.True(t, IsCancelled(<-done))
}
func TestClientPolicy_StreamLifetime(t *testing.T) {
	s := &policyServer{streamStarted: make(chan struct{})}
	c, err := NewClient(Config{Address: policyAddress(t, s), Timeout: 50 * time.Millisecond, RetryPolicy: &types.RetryPolicy{MaxRetries: 2, InitialWait: time.Millisecond}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := c.Sandboxes().Watch(ctx, "default", "sb")
	require.NoError(t, err)
	defer w.Stop()
	<-w.ResultChan()
	done := make(chan error, 1)
	go func() { _, err := c.Exec().Run(ctx, "default", "sb", []string{"sleep"}); done <- err }()
	select {
	case <-s.streamStarted:
	case <-time.After(time.Second):
		t.Fatal("exec did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("exec ended before cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case event := <-w.ResultChan():
		t.Fatalf("watch ended before cancellation: %+v", event)
	default:
	}
	cancel()
	select {
	case err := <-done:
		assert.True(t, IsCancelled(err))
	case <-time.After(time.Second):
		t.Fatal("exec did not cancel")
	}
	assert.Eventually(t, func() bool {
		select {
		case _, ok := <-w.ResultChan():
			return !ok
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	assert.Equal(t, int32(2), s.streamCalls.Load())
}

func TestClientPolicy_StreamFailureNotReplayed(t *testing.T) {
	s := &policyServer{failExec: true}
	c, err := NewClient(Config{Address: policyAddress(t, s), RetryPolicy: &types.RetryPolicy{MaxRetries: 3, InitialWait: time.Millisecond}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = c.Exec().Run(ctx, "default", "sb", []string{"true"})
	require.Error(t, err)
	assert.False(t, IsCancelled(err), "stream must fail on its own, not by cancellation: %v", err)
	assert.Equal(t, int32(1), s.streamCalls.Load())
}
func TestClientPolicy_PollingLifetime(t *testing.T) {
	c := newPolicyTestClient(t, &policyServer{}, 40*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Sandboxes().WaitReady(ctx, "default", "sb", WaitOptions{PollInterval: 10 * time.Millisecond})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("poll ended before cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		assert.True(t, IsCancelled(err))
	case <-time.After(time.Second):
		t.Fatal("poll did not cancel")
	}
}

func TestClientPolicy_Retry(t *testing.T) {
	var calls atomic.Int32
	s := &policyServer{health: func(context.Context) (*pb.HealthResponse, error) {
		if calls.Add(1) == 1 {
			return nil, status.Error(codes.Unavailable, "transient")
		}
		return &pb.HealthResponse{}, nil
	}}
	c, err := NewClient(Config{Address: policyAddress(t, s), RetryPolicy: &types.RetryPolicy{MaxRetries: 1, InitialWait: time.Millisecond}})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()
	_, err = c.Health().Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

type transportPolicyLogger struct {
	mu     sync.Mutex
	output []string
}

func (l *transportPolicyLogger) Debug(message string, fields ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.output = append(l.output, fmt.Sprint(message, fields))
}
func (l *transportPolicyLogger) Info(message string, fields ...any) { l.Debug(message, fields...) }
func (l *transportPolicyLogger) Error(err error, message string, fields ...any) {
	l.Debug(fmt.Sprint(message, err), fields...)
}

type policyAuth struct{}

func (policyAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "AUTH_SECRET"}, nil
}
func (policyAuth) RequireTransportSecurity() bool { return false }
func TestClientPolicy_LogSanitization(t *testing.T) {
	logger := &transportPolicyLogger{}
	var calls atomic.Int32
	s := &policyServer{health: func(ctx context.Context) (*pb.HealthResponse, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		assert.Equal(t, []string{"AUTH_SECRET"}, md.Get("authorization"))
		if calls.Add(1) == 1 {
			return &pb.HealthResponse{Version: "RESPONSE_SECRET"}, nil
		}
		return nil, status.Error(codes.Internal, "SERVER_SECRET")
	}}
	c, err := NewClient(Config{Address: policyAddress(t, s), Auth: policyAuth{}, Logger: logger})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()
	_, err = c.Health().Check(context.Background())
	require.NoError(t, err)
	_, err = c.Health().Check(context.Background())
	require.ErrorContains(t, err, "SERVER_SECRET")
	_, err = c.Sandboxes().Get(context.Background(), "default", "REQUEST_SECRET")
	require.NoError(t, err)
	require.Len(t, logger.output, 3)
	captured := fmt.Sprint(logger.output)
	for _, secret := range []string{"AUTH_SECRET", "RESPONSE_SECRET", "SERVER_SECRET", "REQUEST_SECRET"} {
		assert.NotContains(t, captured, secret)
	}
	assert.Contains(t, captured, "RPC completed")
	assert.Contains(t, captured, "duration")
	assert.Contains(t, captured, "status")
	assert.Contains(t, captured, "attempts")
}

func TestClientPolicy_ActiveCancellation(t *testing.T) {
	started := make(chan struct{})
	s := &policyServer{health: func(ctx context.Context) (*pb.HealthResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}}
	c := newPolicyTestClient(t, s, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Health().Check(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("unary did not start")
	}
	cancel()
	select {
	case err := <-done:
		assert.True(t, IsCancelled(err))
	case <-time.After(time.Second):
		t.Fatal("unary did not cancel")
	}
}
