// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"io"
	"sync"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/converter"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"google.golang.org/grpc"
)

// ServiceRouteSnapshot is a complete replacement view of workspace installations.
type ServiceRouteSnapshot = types.ServiceRouteSnapshot

// DiscoveredServiceRoute identifies an installed service and its current supervisor.
type DiscoveredServiceRoute = types.DiscoveredServiceRoute

// ServiceRouteEndpoint is the trusted supervisor TLS endpoint.
type ServiceRouteEndpoint = types.ServiceRouteEndpoint

// ServiceRouteWatch delivers full replacements. Call Recv from one goroutine;
// Close is safe concurrently. EOF is normal bounded stream completion: reconnect
// and relist. Errors are not empty replacements. Expire cached routes independently.
type ServiceRouteWatch interface {
	Recv() (*ServiceRouteSnapshot, error)
	Close()
}

// ServiceDiscoveryInterface is a narrow interface for external discovery consumers.
// It supports one explicit workspace and does not request all-workspaces grants.
type ServiceDiscoveryInterface interface {
	// Snapshot reads a complete route view for an explicit workspace.
	Snapshot(ctx context.Context, workspace string) (*ServiceRouteSnapshot, error)
	// Watch starts a bounded stream of complete replacement views. The caller
	// must close the watch and reconnect after EOF; the SDK does not reconnect.
	Watch(ctx context.Context, workspace string) (ServiceRouteWatch, error)
}

type serviceDiscoveryClient struct{ client pb.ServiceDiscoveryClient }

func newServiceDiscoveryClient(conn grpc.ClientConnInterface) *serviceDiscoveryClient {
	return &serviceDiscoveryClient{client: pb.NewServiceDiscoveryClient(conn)}
}

func (s *serviceDiscoveryClient) Snapshot(ctx context.Context, workspace string) (*ServiceRouteSnapshot, error) {
	if workspace == "" {
		return nil, &StatusError{Code: ErrorInvalidArgument, Message: "explicit workspace is required"}
	}
	response, err := s.client.GetServiceRouteSnapshot(ctx, &pb.GetServiceRouteSnapshotRequest{WorkspaceScope: namedWorkspaceScope(workspace)})
	if err != nil {
		return nil, converter.FromGRPCError(err)
	}
	return converter.ServiceRouteSnapshotFromProto(response.GetSnapshot(), workspace)
}

func (s *serviceDiscoveryClient) Watch(ctx context.Context, workspace string) (ServiceRouteWatch, error) {
	if workspace == "" {
		return nil, &StatusError{Code: ErrorInvalidArgument, Message: "explicit workspace is required"}
	}
	ctx, cancel := context.WithCancel(ctx)
	stream, err := s.client.WatchServiceRoutes(ctx, &pb.WatchServiceRoutesRequest{WorkspaceScope: namedWorkspaceScope(workspace)})
	if err != nil {
		cancel()
		return nil, converter.FromGRPCError(err)
	}
	return &serviceRouteWatch{stream: stream, cancel: cancel, workspace: workspace}, nil
}

type serviceRouteWatch struct {
	stream    grpc.ServerStreamingClient[pb.WatchServiceRoutesResponse]
	cancel    context.CancelFunc
	workspace string
	once      sync.Once
	err       error
}

func (w *serviceRouteWatch) Close() { w.once.Do(w.cancel) }

func (w *serviceRouteWatch) Recv() (*ServiceRouteSnapshot, error) {
	if w.err != nil {
		return nil, w.err
	}
	response, err := w.stream.Recv()
	if err != nil {
		w.Close()
		if err == io.EOF {
			w.err = io.EOF
		} else {
			w.err = converter.FromGRPCError(err)
		}
		return nil, w.err
	}
	result, err := converter.ServiceRouteSnapshotFromProto(response.GetSnapshot(), w.workspace)
	if err != nil {
		w.err = err
		w.Close()
	}
	return result, err
}

// ServiceRoutes returns the separate application ingress discovery client.
func (c *Client) ServiceRoutes() ServiceDiscoveryInterface { return c.serviceRoutes }
