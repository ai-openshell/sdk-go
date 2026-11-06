// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package types

// Logger defines structured logging for the SDK. Compatible with logr.Logger
// and slog.Logger adapters. Implementations must support concurrent calls.
// Unary RPCs emit one completion with method, total duration, final status, and
// attempt count: Debug on success, Error on failure. Scheduled retries use Debug
// with method, upcoming attempt, and delay. Failure error arguments contain only
// a sanitized status code. Bodies, credentials, auth metadata and raw server
// errors are never logged. This contract excludes stream lifecycle and global
// gRPC logging.
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Error(err error, msg string, keysAndValues ...any)
}
