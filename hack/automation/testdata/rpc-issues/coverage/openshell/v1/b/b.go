// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package b binds no service, so its Snapshot call covers nothing.
package b

func snapshot(client any) {
	client.Snapshot(nil)
}
