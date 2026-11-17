// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package types

import "time"

// Config holds all settings needed to create a Client.
type Config struct {
	Address string
	TLS     *TLSConfig
	Auth    AuthProvider
	// Timeout supplies a default deadline for unary RPCs without a caller deadline.
	// Explicit caller deadlines take precedence, even when longer. All attempts
	// and retry waits share one budget. Zero disables the default; negative values
	// fail construction with ErrorInvalidArgument. Streams (including Exec.Run),
	// sessions and polling helpers retain caller-controlled overall lifetimes;
	// their individual unary setup/poll requests still follow this default.
	// Timeout also bounds mutations, and a mutation cut off by it may still have
	// been applied on the gateway: a CreateSandbox that fails with
	// DeadlineExceeded may have created the sandbox, so repeating it can return
	// AlreadyExists.
	Timeout time.Duration
	// RetryPolicy is copied at construction. Nil disables SDK-configured retries.
	// Service-config retries are disabled; gRPC transparent retries before remote
	// application processing remain a transport behavior.
	RetryPolicy *RetryPolicy
	// Logger receives sanitized unary completion/retry diagnostics. Nil disables
	// these diagnostics. Implementations must support concurrent calls.
	// Completions log at Error only for Unavailable, Internal, Unknown,
	// DeadlineExceeded and DataLoss; statuses that mean the gateway answered the
	// request, such as NotFound from a WaitDeleted poll, log at Debug.
	// Logger does not reach token refresh: RefreshableToken logs its stale-token
	// warnings only through its own WithLogger option.
	Logger Logger
}
