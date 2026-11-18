// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package c binds a service whose name starts with "New".
package c

func get(conn any) {
	client := pb.NewNewsFeedClient(conn)
	client.Get(nil)
}
