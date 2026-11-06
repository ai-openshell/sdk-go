// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"time"

	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type clientPolicy struct {
	timeout time.Duration
	retry   *types.RetryPolicy
	logger  types.Logger
}

func newClientPolicy(timeout time.Duration, retry *types.RetryPolicy, logger types.Logger) (*clientPolicy, error) {
	invalid := func(message string) (*clientPolicy, error) {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: message}
	}
	if timeout < 0 {
		return invalid("timeout must not be negative")
	}
	p := &clientPolicy{timeout: timeout, logger: logger}
	if retry != nil {
		copied := *retry
		if copied.MaxRetries < 0 || copied.InitialWait < 0 || copied.MaxWait < 0 {
			return invalid("retry fields must not be negative")
		}
		if copied.InitialWait == 0 {
			copied.InitialWait = 100 * time.Millisecond
		}
		if copied.MaxWait == 0 {
			copied.MaxWait = 5 * time.Second
		}
		if copied.MaxWait < copied.InitialWait {
			return invalid("retry maximum wait must be at least the initial wait")
		}
		p.retry = &copied
	}
	return p, nil
}

// ClientOptions validates and snapshots request policy settings.
// NewConnection disables gRPC's built-in retries after all caller options,
// so the interceptor returned here is the only retry mechanism.
func ClientOptions(timeout time.Duration, retry *types.RetryPolicy, logger types.Logger) ([]grpc.DialOption, error) {
	p, err := newClientPolicy(timeout, retry, logger)
	if err != nil {
		return nil, err
	}
	return []grpc.DialOption{grpc.WithChainUnaryInterceptor(p.unary)}, nil
}

func (p *clientPolicy) unary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) (result error) {
	started := time.Now()
	var attempts uint64
	defer func() {
		if p.logger == nil {
			return
		}
		code := status.Code(result)
		fields := []any{"method", method, "duration", time.Since(started), "status", code.String(), "attempts", attempts}
		if result == nil {
			p.logger.Debug("RPC completed", fields...)
		} else {
			p.logger.Error(status.Error(code, ""), "RPC completed", fields...)
		}
	}()
	if _, ok := ctx.Deadline(); !ok && p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	delay := time.Duration(0)
	if p.retry != nil {
		delay = p.retry.InitialWait
	}
	for retries := 0; ; {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		attempts++
		err := invoke(ctx, method, req, reply, cc, opts...)
		if p.retry == nil || retries >= p.retry.MaxRetries || !safeReadMethods[method] || status.Code(err) != codes.Unavailable {
			return err
		}
		if p.logger != nil {
			p.logger.Debug("RPC retry", "method", method, "attempt", attempts+1, "delay", delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
		retries++
		delay = nextDelay(delay, p.retry.MaxWait)
	}
}

func nextDelay(current, maximum time.Duration) time.Duration {
	if current >= maximum-current {
		return maximum
	}
	return current * 2
}

// Only methods verified to have no application side effects may be replayed.
var safeReadMethods = map[string]bool{
	pb.OpenShell_Health_FullMethodName:                   true,
	pb.OpenShell_GetCurrentUser_FullMethodName:           true,
	pb.OpenShell_GetGatewayInfo_FullMethodName:           true,
	pb.OpenShell_GetSandbox_FullMethodName:               true,
	pb.OpenShell_ListSandboxes_FullMethodName:            true,
	pb.OpenShell_GetSandboxTemplate_FullMethodName:       true,
	pb.OpenShell_ListSandboxTemplates_FullMethodName:     true,
	pb.OpenShell_ListSandboxProviders_FullMethodName:     true,
	pb.OpenShell_GetService_FullMethodName:               true,
	pb.OpenShell_ListServices_FullMethodName:             true,
	pb.OpenShell_GetProvider_FullMethodName:              true,
	pb.OpenShell_ListProviders_FullMethodName:            true,
	pb.OpenShell_GetProviderRefreshStatus_FullMethodName: true,
	pb.OpenShell_GetGatewayConfig_FullMethodName:         true,
	pb.OpenShell_GetSandboxPolicyStatus_FullMethodName:   true,
	pb.OpenShell_ListSandboxPolicies_FullMethodName:      true,
	pb.OpenShell_GetSandboxLogs_FullMethodName:           true,
	pb.OpenShell_GetDraftPolicy_FullMethodName:           true,
	pb.OpenShell_GetDraftHistory_FullMethodName:          true,
	pb.OpenShell_GetWorkspace_FullMethodName:             true,
	pb.OpenShell_ListWorkspaces_FullMethodName:           true,
	pb.OpenShell_ListWorkspaceMembers_FullMethodName:     true,
}
