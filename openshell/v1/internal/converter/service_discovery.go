// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converter

import (
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
)

// ServiceRouteSnapshotFromProto validates and copies a full discovery view.
func ServiceRouteSnapshotFromProto(snapshot *pb.ServiceRouteSnapshot, workspace string) (*types.ServiceRouteSnapshot, error) {
	invalid := func() (*types.ServiceRouteSnapshot, error) {
		return nil, &types.StatusError{Code: types.ErrorInternal, Message: "invalid service discovery snapshot"}
	}
	if snapshot == nil || snapshot.GetRevision() == "" || len(snapshot.GetRoutes()) > 4096 {
		return invalid()
	}
	result := &types.ServiceRouteSnapshot{Revision: snapshot.GetRevision(), Routes: make([]types.DiscoveredServiceRoute, 0, len(snapshot.GetRoutes()))}
	hostnames := make(map[string]bool, len(snapshot.GetRoutes()))
	for _, route := range snapshot.GetRoutes() {
		if route == nil || route.GetWorkspace() != workspace ||
			route.GetSandbox() == "" || route.GetHostname() == "" ||
			route.GetInstallationVersion() == "" || hostnames[route.GetHostname()] {
			return invalid()
		}
		endpoint := route.GetEndpoint()
		if endpoint == nil || endpoint.GetHost() == "" || endpoint.GetPort() == 0 || endpoint.GetPort() > 65535 {
			return invalid()
		}
		if route.GetExpiresAt() == nil || route.GetExpiresAt().CheckValid() != nil {
			return invalid()
		}
		hostnames[route.GetHostname()] = true
		result.Routes = append(result.Routes, types.DiscoveredServiceRoute{
			Workspace: route.GetWorkspace(), Sandbox: route.GetSandbox(), Name: route.GetName(), Hostname: route.GetHostname(),
			Endpoint:            types.ServiceRouteEndpoint{Host: route.GetEndpoint().GetHost(), Port: route.GetEndpoint().GetPort()},
			InstallationVersion: route.GetInstallationVersion(), DeclarationRevision: route.GetDeclarationRevision(), ExpiresAt: route.GetExpiresAt().AsTime(),
		})
	}
	return result, nil
}
