// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// Logger defines structured logging for the SDK. Compatible with logr.Logger
// and slog.Logger adapters. Implementations must support concurrent calls.
// See [types.Logger] for the diagnostics the SDK emits and what it never logs.
type Logger = types.Logger
