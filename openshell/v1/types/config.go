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
	Timeout time.Duration
	// RetryPolicy is copied at construction. Nil disables SDK-configured retries.
	// Service-config retries are disabled; gRPC transparent retries before remote
	// application processing remain a transport behavior.
	RetryPolicy *RetryPolicy
	// Logger receives sanitized unary completion/retry diagnostics. Nil disables
	// these diagnostics. Implementations must support concurrent calls.
	Logger Logger
}
