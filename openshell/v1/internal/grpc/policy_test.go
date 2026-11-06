// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestClientOptions_Validation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		retry   *types.RetryPolicy
		invalid bool
	}{
		{name: "nil"}, {name: "zero", retry: &types.RetryPolicy{}},
		{name: "negative timeout", timeout: -1, invalid: true},
		{name: "negative retries", retry: &types.RetryPolicy{MaxRetries: -1}, invalid: true},
		{name: "negative initial", retry: &types.RetryPolicy{InitialWait: -1}, invalid: true},
		{name: "negative maximum", retry: &types.RetryPolicy{MaxWait: -1}, invalid: true},
		{name: "reversed", retry: &types.RetryPolicy{InitialWait: time.Second, MaxWait: time.Millisecond}, invalid: true},
		{name: "default initial reverses bounds", retry: &types.RetryPolicy{MaxWait: time.Millisecond}, invalid: true},
		{name: "default maximum reverses bounds", retry: &types.RetryPolicy{InitialWait: 6 * time.Second}, invalid: true},
		{name: "maximum duration", retry: &types.RetryPolicy{MaxRetries: math.MaxInt, InitialWait: math.MaxInt64, MaxWait: math.MaxInt64}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ClientOptions(tc.timeout, tc.retry, nil)
			if tc.invalid {
				assert.True(t, types.IsInvalidArgument(err), "%v", err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestClientOptions_Snapshot(t *testing.T) {
	caller := &types.RetryPolicy{MaxRetries: 2}
	original := *caller
	p, err := newClientPolicy(0, caller, nil)
	require.NoError(t, err)
	assert.Equal(t, original, *caller)
	assert.Equal(t, 100*time.Millisecond, p.retry.InitialWait)
	assert.Equal(t, 5*time.Second, p.retry.MaxWait)
	caller.MaxRetries = 50
	assert.Equal(t, 2, p.retry.MaxRetries)
}

func TestClientPolicy_CallerDeadline(t *testing.T) {
	for _, duration := range []time.Duration{0, 10 * time.Millisecond, time.Second} {
		t.Run(duration.String(), func(t *testing.T) {
			p, err := newClientPolicy(100*time.Millisecond, nil, nil)
			require.NoError(t, err)
			ctx := context.Background()
			var cancel context.CancelFunc
			if duration > 0 {
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}
			started := time.Now()
			err = p.unary(ctx, "method", nil, nil, nil, func(actual context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
				deadline, ok := actual.Deadline()
				require.True(t, ok)
				if duration > 0 {
					expected, _ := ctx.Deadline()
					assert.Equal(t, expected, deadline)
				} else {
					assert.WithinDuration(t, started.Add(100*time.Millisecond), deadline, 20*time.Millisecond)
				}
				return nil
			})
			require.NoError(t, err)
		})
	}
	p, _ := newClientPolicy(0, nil, nil)
	require.NoError(t, p.unary(context.Background(), "method", nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		_, ok := ctx.Deadline()
		assert.False(t, ok)
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := p.unary(ctx, "method", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		calls++
		return nil
	})
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.Zero(t, calls)
}

// expectedSafeReads is the independently maintained list of RPCs that may be
// retried. It is written out literally so that a change to safeReadMethods
// (for example swapping in a mutating RPC) fails this test.
var expectedSafeReads = map[string]bool{
	"/openshell.v1.OpenShell/Health":                   true,
	"/openshell.v1.OpenShell/GetCurrentUser":           true,
	"/openshell.v1.OpenShell/GetGatewayInfo":           true,
	"/openshell.v1.OpenShell/GetSandbox":               true,
	"/openshell.v1.OpenShell/ListSandboxes":            true,
	"/openshell.v1.OpenShell/GetSandboxTemplate":       true,
	"/openshell.v1.OpenShell/ListSandboxTemplates":     true,
	"/openshell.v1.OpenShell/ListSandboxProviders":     true,
	"/openshell.v1.OpenShell/GetService":               true,
	"/openshell.v1.OpenShell/ListServices":             true,
	"/openshell.v1.OpenShell/GetProvider":              true,
	"/openshell.v1.OpenShell/ListProviders":            true,
	"/openshell.v1.OpenShell/GetProviderRefreshStatus": true,
	"/openshell.v1.OpenShell/GetGatewayConfig":         true,
	"/openshell.v1.OpenShell/GetSandboxPolicyStatus":   true,
	"/openshell.v1.OpenShell/ListSandboxPolicies":      true,
	"/openshell.v1.OpenShell/GetSandboxLogs":           true,
	"/openshell.v1.OpenShell/GetDraftPolicy":           true,
	"/openshell.v1.OpenShell/GetDraftHistory":          true,
	"/openshell.v1.OpenShell/GetWorkspace":             true,
	"/openshell.v1.OpenShell/ListWorkspaces":           true,
	"/openshell.v1.OpenShell/ListWorkspaceMembers":     true,
}

func TestClientPolicy_Retry(t *testing.T) {
	require.Equal(t, expectedSafeReads, safeReadMethods)
	for _, method := range pb.OpenShell_ServiceDesc.Methods {
		t.Run(method.MethodName, func(t *testing.T) {
			full := "/" + pb.OpenShell_ServiceDesc.ServiceName + "/" + method.MethodName
			p, _ := newClientPolicy(0, &types.RetryPolicy{MaxRetries: 1, InitialWait: time.Nanosecond}, nil)
			calls := 0
			err := p.unary(context.Background(), full, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				if calls == 1 {
					return status.Error(codes.Unavailable, "transient")
				}
				return nil
			})
			if expectedSafeReads[full] {
				assert.Equal(t, 2, calls)
				require.NoError(t, err)
			} else {
				assert.Equal(t, 1, calls)
				assert.Equal(t, codes.Unavailable, status.Code(err))
			}
		})
	}
	for _, tc := range []struct {
		name     string
		policy   *types.RetryPolicy
		code     codes.Code
		method   string
		attempts int
	}{
		{"exhausted", &types.RetryPolicy{MaxRetries: 2, InitialWait: 1}, codes.Unavailable, pb.OpenShell_Health_FullMethodName, 3},
		{"nil", nil, codes.Unavailable, pb.OpenShell_Health_FullMethodName, 1},
		{"zero", &types.RetryPolicy{}, codes.Unavailable, pb.OpenShell_Health_FullMethodName, 1},
		{"resource exhausted", &types.RetryPolicy{MaxRetries: 2}, codes.ResourceExhausted, pb.OpenShell_Health_FullMethodName, 1},
		{"unknown", &types.RetryPolicy{MaxRetries: 2}, codes.Unavailable, "/unknown", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newClientPolicy(0, tc.policy, nil)
			calls := 0
			err := p.unary(context.Background(), tc.method, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				return status.Error(tc.code, "transient")
			})
			assert.Equal(t, tc.attempts, calls)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}
}
func TestClientPolicy_Backoff(t *testing.T) {
	delay := 10 * time.Millisecond
	var delays []time.Duration
	for range 4 {
		delays = append(delays, delay)
		delay = nextDelay(delay, 25*time.Millisecond)
	}
	assert.Equal(t, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond, 25 * time.Millisecond}, delays)
	assert.Equal(t, time.Duration(math.MaxInt64), nextDelay(math.MaxInt64/2+1, math.MaxInt64))
}
func TestClientPolicy_RetryBudget(t *testing.T) {
	for _, cancelInstead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelInstead), func(t *testing.T) {
			p, _ := newClientPolicy(40*time.Millisecond, &types.RetryPolicy{MaxRetries: math.MaxInt, InitialWait: time.Duration(math.MaxInt64), MaxWait: time.Duration(math.MaxInt64)}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			start := time.Now()
			err := p.unary(ctx, pb.OpenShell_Health_FullMethodName, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				if cancelInstead {
					time.AfterFunc(10*time.Millisecond, cancel)
				}
				return status.Error(codes.Unavailable, "transient")
			})
			expected := codes.DeadlineExceeded
			if cancelInstead {
				expected = codes.Canceled
			}
			assert.Equal(t, expected, status.Code(err))
			assert.Equal(t, 1, calls)
			assert.Less(t, time.Since(start), time.Second)
		})
	}
}
func TestClientPolicy_RetrySnapshotConcurrent(t *testing.T) {
	original := &types.RetryPolicy{MaxRetries: 2, InitialWait: 1}
	p, _ := newClientPolicy(0, original, nil)
	original.MaxRetries = 0
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			calls := 0
			err := p.unary(context.Background(), pb.OpenShell_Health_FullMethodName, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				return status.Error(codes.Unavailable, "transient")
			})
			assert.Equal(t, codes.Unavailable, status.Code(err))
			assert.Equal(t, 3, calls)
		})
	}
	wg.Wait()
}

type retryExclusionServer struct {
	pb.UnimplementedOpenShellServer
	calls atomic.Int32
}

func (s *retryExclusionServer) DeleteSandbox(context.Context, *pb.DeleteSandboxRequest) (*pb.DeleteSandboxResponse, error) {
	s.calls.Add(1)
	return nil, status.Error(codes.Unavailable, "transient")
}
func TestClientPolicy_RetryExclusions_ServiceConfig(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	handler := &retryExclusionServer{}
	pb.RegisterOpenShellServer(server, handler)
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()
	policy, err := ClientOptions(0, &types.RetryPolicy{MaxRetries: 3, InitialWait: 1}, nil)
	require.NoError(t, err)
	options := append(policy, grpc.WithDefaultServiceConfig(`{"methodConfig":[{"name":[{"service":"openshell.v1.OpenShell"}],"retryPolicy":{"MaxAttempts":4,"InitialBackoff":"0.001s","MaxBackoff":"0.001s","BackoffMultiplier":1,"RetryableStatusCodes":["UNAVAILABLE"]}}]}`))
	conn, err := NewConnection("http://"+lis.Addr().String(), nil, nil, options...)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = pb.NewOpenShellClient(conn).DeleteSandbox(ctx, &pb.DeleteSandboxRequest{})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, int32(1), handler.calls.Load())
}

type policyLogEntry struct {
	level, message string
	err            error
	fields         []any
}
type policyLogger struct {
	mu      sync.Mutex
	entries []policyLogEntry
}

func (l *policyLogger) record(level string, err error, message string, fields []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, policyLogEntry{level, message, err, append([]any(nil), fields...)})
}
func (l *policyLogger) Debug(message string, fields ...any) { l.record("debug", nil, message, fields) }
func (l *policyLogger) Info(message string, fields ...any)  { l.record("info", nil, message, fields) }
func (l *policyLogger) Error(err error, message string, fields ...any) {
	l.record("error", err, message, fields)
}
func logField(entry policyLogEntry, key string) any {
	for i := 0; i < len(entry.fields); i += 2 {
		if entry.fields[i] == key {
			return entry.fields[i+1]
		}
	}
	return nil
}
func TestClientPolicy_Logging(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			logger := &policyLogger{}
			p, _ := newClientPolicy(0, &types.RetryPolicy{MaxRetries: 2, InitialWait: time.Millisecond}, logger)
			calls := 0
			started := time.Now()
			err := p.unary(context.Background(), pb.OpenShell_Health_FullMethodName, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				if failure || calls < 3 {
					return status.Error(codes.Unavailable, "secret server message")
				}
				return nil
			})
			assert.Equal(t, 3, calls)
			require.Len(t, logger.entries, 3)
			for i, entry := range logger.entries[:2] {
				assert.Equal(t, "debug", entry.level)
				assert.Equal(t, "RPC retry", entry.message)
				assert.Equal(t, uint64(i+2), logField(entry, "attempt"))
				assert.Equal(t, time.Millisecond*time.Duration(1<<i), logField(entry, "delay"))
				assert.Equal(t, pb.OpenShell_Health_FullMethodName, logField(entry, "method"))
			}
			completion := logger.entries[2]
			assert.Equal(t, "RPC completed", completion.message)
			assert.Equal(t, uint64(3), logField(completion, "attempts"))
			assert.LessOrEqual(t, logField(completion, "duration").(time.Duration), time.Since(started))
			assert.GreaterOrEqual(t, logField(completion, "duration").(time.Duration), 3*time.Millisecond)
			if failure {
				assert.Equal(t, "error", completion.level)
				assert.Equal(t, codes.Unavailable.String(), logField(completion, "status"))
				require.Error(t, err)
				assert.Equal(t, codes.Unavailable, status.Code(completion.err))
			} else {
				assert.Equal(t, "debug", completion.level)
				assert.Equal(t, "OK", logField(completion, "status"))
				require.NoError(t, err)
			}
		})
	}
	logger := &policyLogger{}
	p, _ := newClientPolicy(0, nil, logger)
	require.NoError(t, p.unary(context.Background(), "method", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }))
	require.Len(t, logger.entries, 1)
}
func TestClientPolicy_LogSanitization(t *testing.T) {
	for _, failure := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprint(failure, canceled), func(t *testing.T) {
				logger := &policyLogger{}
				p, _ := newClientPolicy(0, nil, logger)
				ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "AUTH_SECRET")))
				defer cancel()
				if canceled {
					cancel()
				}
				_ = p.unary(ctx, pb.OpenShell_Health_FullMethodName, map[string]string{"request": "REQUEST_SECRET"}, map[string]string{"response": "RESPONSE_SECRET"}, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
					if failure {
						return status.Error(codes.Internal, "SERVER_SECRET")
					}
					return nil
				})
				require.Len(t, logger.entries, 1)
				captured := fmt.Sprintf("%+v", logger.entries)
				for _, entry := range logger.entries {
					if entry.err != nil {
						captured += entry.err.Error()
					}
					captured += fmt.Sprint(entry.fields)
				}
				for _, secret := range []string{"AUTH_SECRET", "REQUEST_SECRET", "RESPONSE_SECRET", "SERVER_SECRET"} {
					assert.NotContains(t, captured, secret)
				}
				if canceled {
					assert.Equal(t, uint64(0), logField(logger.entries[0], "attempts"))
					assert.Equal(t, codes.Canceled, status.Code(logger.entries[0].err))
				}
			})
		}
	}
	logger := &policyLogger{}
	p, _ := newClientPolicy(0, nil, logger)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_ = p.unary(context.Background(), "method", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil })
		})
	}
	wg.Wait()
	assert.Len(t, logger.entries, 10)
}

func TestClientPolicy_RetryBudgetAcrossAttempts(t *testing.T) {
	p, _ := newClientPolicy(70*time.Millisecond, &types.RetryPolicy{MaxRetries: 10, InitialWait: 10 * time.Millisecond, MaxWait: 40 * time.Millisecond}, nil)
	var deadlines []time.Time
	start := time.Now()
	err := p.unary(context.Background(), pb.OpenShell_Health_FullMethodName, nil, nil, nil, func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		return status.Error(codes.Unavailable, "transient")
	})
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	assert.Len(t, deadlines, 3)
	for _, deadline := range deadlines {
		assert.Equal(t, deadlines[0], deadline)
	}
	assert.Less(t, time.Since(start), time.Second)
}

func TestClientPolicy_BackoffScheduledDelays(t *testing.T) {
	logger := &policyLogger{}
	p, _ := newClientPolicy(0, &types.RetryPolicy{MaxRetries: 4, InitialWait: 10 * time.Millisecond, MaxWait: 25 * time.Millisecond}, logger)
	started := time.Now()
	err := p.unary(context.Background(), pb.OpenShell_Health_FullMethodName, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.Unavailable, "transient")
	})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	require.Len(t, logger.entries, 5)
	var delays []time.Duration
	for _, entry := range logger.entries[:4] {
		delays = append(delays, logField(entry, "delay").(time.Duration))
	}
	assert.Equal(t, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond, 25 * time.Millisecond}, delays)
	assert.GreaterOrEqual(t, time.Since(started), 80*time.Millisecond)
}
