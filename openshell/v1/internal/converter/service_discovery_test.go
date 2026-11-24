// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converter

import (
	"testing"
	"time"

	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDiscoverySnapshotCopiesAndRejectsIncompleteViews(t *testing.T) {
	wire := &pb.ServiceRouteSnapshot{Revision: "revision", Routes: []*pb.DiscoveredServiceRoute{{
		Workspace: "default", Sandbox: "demo", Hostname: "demo.example.com", InstallationVersion: "owner-1",
		Endpoint: &pb.ServiceRouteEndpoint{Host: "10.0.0.1", Port: 9443}, ExpiresAt: timestamppb.New(time.Now()),
	}}}
	snapshot, err := ServiceRouteSnapshotFromProto(wire, "default")
	require.NoError(t, err)
	snapshot.Routes[0].Endpoint.Host = "changed"
	require.Equal(t, "10.0.0.1", wire.Routes[0].Endpoint.Host)
	wire.Routes[0].Hostname = "different.example.com"
	require.Equal(t, "demo.example.com", snapshot.Routes[0].Hostname)
	_, err = ServiceRouteSnapshotFromProto(wire, "other")
	require.Error(t, err)
	wire.Routes[0].ExpiresAt = nil
	_, err = ServiceRouteSnapshotFromProto(wire, "default")
	require.Error(t, err)
	_, err = ServiceRouteSnapshotFromProto(nil, "default")
	require.Error(t, err)
}

func TestDiscoverySnapshotRejectsMalformedRoutes(t *testing.T) {
	valid := func() *pb.ServiceRouteSnapshot {
		return &pb.ServiceRouteSnapshot{Revision: "opaque", Routes: []*pb.DiscoveredServiceRoute{{
			Workspace: "default", Sandbox: "demo", Hostname: "demo.example.com", InstallationVersion: "owner-1",
			Endpoint: &pb.ServiceRouteEndpoint{Host: "10.0.0.1", Port: 9443}, ExpiresAt: timestamppb.New(time.Now()),
		}}}
	}
	cases := map[string]func(*pb.ServiceRouteSnapshot){
		"missing revision":     func(s *pb.ServiceRouteSnapshot) { s.Revision = "" },
		"nil route":            func(s *pb.ServiceRouteSnapshot) { s.Routes[0] = nil },
		"wrong workspace":      func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Workspace = "other" },
		"missing sandbox":      func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Sandbox = "" },
		"missing hostname":     func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Hostname = "" },
		"missing installation": func(s *pb.ServiceRouteSnapshot) { s.Routes[0].InstallationVersion = "" },
		"missing endpoint":     func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Endpoint = nil },
		"missing host":         func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Endpoint.Host = "" },
		"zero port":            func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Endpoint.Port = 0 },
		"oversized port":       func(s *pb.ServiceRouteSnapshot) { s.Routes[0].Endpoint.Port = 65536 },
		"missing expiry":       func(s *pb.ServiceRouteSnapshot) { s.Routes[0].ExpiresAt = nil },
		"invalid expiry":       func(s *pb.ServiceRouteSnapshot) { s.Routes[0].ExpiresAt.Nanos = -1 },
		"duplicate hostname": func(s *pb.ServiceRouteSnapshot) {
			s.Routes = append(s.Routes, proto.Clone(s.Routes[0]).(*pb.DiscoveredServiceRoute))
		},
		"oversized snapshot": func(s *pb.ServiceRouteSnapshot) { s.Routes = make([]*pb.DiscoveredServiceRoute, 4097) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wire := valid()
			mutate(wire)
			snapshot, err := ServiceRouteSnapshotFromProto(wire, "default")
			require.Error(t, err)
			require.Nil(t, snapshot)
			var status *types.StatusError
			require.ErrorAs(t, err, &status)
			require.Equal(t, types.ErrorInternal, status.Code)
		})
	}
	// Unnamed services and already expired entries remain representable; callers
	// must enforce expiry independently of discovery availability.
	wire := valid()
	wire.Routes[0].ExpiresAt = timestamppb.New(time.Now().Add(-time.Hour))
	snapshot, err := ServiceRouteSnapshotFromProto(wire, "default")
	require.NoError(t, err)
	require.Empty(t, snapshot.Routes[0].Name)
	require.True(t, snapshot.Routes[0].ExpiresAt.Before(time.Now()))
	snapshot, err = ServiceRouteSnapshotFromProto(&pb.ServiceRouteSnapshot{Revision: "empty"}, "default")
	require.NoError(t, err)
	require.Empty(t, snapshot.Routes)
}
