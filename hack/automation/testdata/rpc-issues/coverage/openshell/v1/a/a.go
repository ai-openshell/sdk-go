// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package a binds the OpenShell service and calls Watch.
package a

func watch(conn any) {
	client := pb.NewOpenShellClient(conn)
	client.Watch(nil)
}
