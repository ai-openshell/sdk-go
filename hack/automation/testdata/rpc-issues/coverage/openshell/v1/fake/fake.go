// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package fake is excluded from coverage even though it binds OpenShell.
package fake

func hidden(conn any) {
	client := pb.NewOpenShellClient(conn)
	client.Hidden(nil)
}
