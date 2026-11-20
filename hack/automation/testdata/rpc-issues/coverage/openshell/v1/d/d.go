// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package d binds the OpenShell service and passes Exec and Forward as
// method values to a routing helper, as the bidi stream wrappers do.
package d

func stream(conn any) {
	client := pb.NewOpenShellClient(conn)
	openRouted(nil, client.Exec)
	openRouted(client.Forward, nil)
}
