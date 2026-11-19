// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	dm "github.com/ai-openshell/sdk-go/proto/datamodelv1"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type discoveryServer struct {
	pb.UnimplementedServiceDiscoveryServer
	views    []*pb.ServiceRouteSnapshot
	terminal error
	block    bool
	canceled chan struct{}
}

func discoveryView(revision string, expires time.Time) *pb.ServiceRouteSnapshot {
	return &pb.ServiceRouteSnapshot{Revision: revision, Routes: []*pb.DiscoveredServiceRoute{{
		Workspace: "default", Sandbox: "demo", Name: "web", Hostname: "demo.example.com",
		Endpoint:            &pb.ServiceRouteEndpoint{Host: "10.0.0.1", Port: 9443},
		InstallationVersion: "owner-1", DeclarationRevision: 7, ExpiresAt: timestamppb.New(expires),
	}}}
}

func (s *discoveryServer) GetServiceRouteSnapshot(_ context.Context, req *pb.GetServiceRouteSnapshotRequest) (*pb.GetServiceRouteSnapshotResponse, error) {
	if req.GetWorkspaceScope().GetWorkspace() != "default" {
		return nil, status.Error(codes.PermissionDenied, "workspace denied")
	}
	return &pb.GetServiceRouteSnapshotResponse{Snapshot: s.views[0]}, nil
}
func (s *discoveryServer) WatchServiceRoutes(req *pb.WatchServiceRoutesRequest, stream grpc.ServerStreamingServer[pb.WatchServiceRoutesResponse]) error {
	if _, ok := req.GetWorkspaceScope().GetSelection().(*dm.WorkspaceSelector_Workspace); !ok {
		return status.Error(codes.InvalidArgument, "explicit workspace required")
	}
	for _, view := range s.views {
		if err := stream.Send(&pb.WatchServiceRoutesResponse{Snapshot: view}); err != nil {
			return err
		}
	}
	if s.block {
		<-stream.Context().Done()
		close(s.canceled)
		return status.FromContextError(stream.Context().Err()).Err()
	}
	return s.terminal
}

func discoveryClientForTest(t *testing.T, mock *discoveryServer) *serviceDiscoveryClient {
	t.Helper()
	listener := bufconn.Listen(bufSize)
	server := grpc.NewServer()
	pb.RegisterServiceDiscoveryServer(server, mock)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufconn", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	return newServiceDiscoveryClient(conn)
}

func TestDiscoverySnapshotAndWatchPreserveRenewalWithdrawalAndEOF(t *testing.T) {
	expiry := time.Now().Add(10 * time.Second).UTC()
	initial := discoveryView("revision-1", expiry)
	renewal := discoveryView("revision-2", expiry.Add(4*time.Second))
	empty := &pb.ServiceRouteSnapshot{Revision: "revision-3"}
	client := discoveryClientForTest(t, &discoveryServer{views: []*pb.ServiceRouteSnapshot{initial, renewal, empty}})
	snapshot, err := client.Snapshot(context.Background(), "default")
	require.NoError(t, err)
	require.Equal(t, "demo.example.com", snapshot.Routes[0].Hostname)
	require.Equal(t, expiry, snapshot.Routes[0].ExpiresAt)
	watch, err := client.Watch(context.Background(), "default")
	require.NoError(t, err)
	defer watch.Close()
	first, err := watch.Recv()
	require.NoError(t, err)
	second, err := watch.Recv()
	require.NoError(t, err)
	require.NotEqual(t, first.Revision, second.Revision)
	require.Equal(t, first.Routes[0].InstallationVersion, second.Routes[0].InstallationVersion)
	require.NotEqual(t, first.Routes[0].ExpiresAt, second.Routes[0].ExpiresAt)
	withdrawn, err := watch.Recv()
	require.NoError(t, err)
	require.Empty(t, withdrawn.Routes)
	none, err := watch.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.Nil(t, none)
	_, err = client.Snapshot(context.Background(), "")
	require.True(t, IsInvalidArgument(err))
	_, err = client.Watch(context.Background(), "")
	require.True(t, IsInvalidArgument(err))
	_, err = client.Snapshot(context.Background(), "other")
	require.True(t, IsPermissionDenied(err))
}

func TestDiscoveryErrorsAreNotEmptySnapshotsAndCloseCancelsRPC(t *testing.T) {
	view := discoveryView("revision", time.Now().Add(time.Second))
	client := discoveryClientForTest(t, &discoveryServer{views: []*pb.ServiceRouteSnapshot{view}, terminal: status.Error(codes.PermissionDenied, "revoked")})
	watch, err := client.Watch(context.Background(), "default")
	require.NoError(t, err)
	_, err = watch.Recv()
	require.NoError(t, err)
	next, err := watch.Recv()
	require.Nil(t, next)
	require.True(t, IsPermissionDenied(err))
	mock := &discoveryServer{views: []*pb.ServiceRouteSnapshot{view}, block: true, canceled: make(chan struct{})}
	client = discoveryClientForTest(t, mock)
	watch, err = client.Watch(context.Background(), "default")
	require.NoError(t, err)
	_, err = watch.Recv()
	require.NoError(t, err)
	watch.Close()
	watch.Close()
	_, err = watch.Recv()
	require.True(t, IsCancelled(err))
	select {
	case <-mock.canceled:
	case <-time.After(time.Second):
		t.Fatal("watch cancellation did not reach server")
	}
}

func TestDiscoveryMalformedWatchIsTerminalAndCancelsRPC(t *testing.T) {
	invalid := discoveryView("invalid", time.Now().Add(time.Second))
	invalid.Routes[0].Workspace = "other"
	mock := &discoveryServer{views: []*pb.ServiceRouteSnapshot{invalid}, block: true, canceled: make(chan struct{})}
	client := discoveryClientForTest(t, mock)
	snapshot, err := client.Snapshot(context.Background(), "default")
	require.Nil(t, snapshot)
	var statusErr *StatusError
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, ErrorInternal, statusErr.Code)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watch, err := client.Watch(ctx, "default")
	require.NoError(t, err)
	defer watch.Close()
	snapshot, err = watch.Recv()
	require.Nil(t, snapshot)
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, ErrorInternal, statusErr.Code)
	snapshot, nextErr := watch.Recv()
	require.Nil(t, snapshot)
	require.Equal(t, err, nextErr)
	select {
	case <-mock.canceled:
	case <-ctx.Done():
		t.Fatal("invalid snapshot did not cancel watch")
	}
}
