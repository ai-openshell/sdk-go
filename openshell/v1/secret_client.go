// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/converter"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"google.golang.org/grpc"
)

type secretClient struct {
	client pb.OpenShellClient
}

var _ SecretInterface = (*secretClient)(nil)

func newSecretClient(conn grpc.ClientConnInterface) *secretClient {
	return &secretClient{client: pb.NewOpenShellClient(conn)}
}

func (s *secretClient) Create(ctx context.Context, workspace string, secret *SecretCreate) (*Secret, error) {
	if secret == nil {
		return nil, &StatusError{Code: ErrorInvalidArgument, Message: "secret must not be nil"}
	}
	req := converter.SecretCreateToProto(secret)
	req.WorkspaceScope = namedWorkspaceScope(workspace)
	resp, err := s.client.CreateSecret(ctx, req)
	if err != nil {
		return nil, converter.FromGRPCError(err)
	}
	return converter.SecretFromProto(resp.GetSecret()), nil
}

func (s *secretClient) Get(ctx context.Context, workspace, name string) (*Secret, error) {
	resp, err := s.client.GetSecret(ctx, &pb.GetSecretRequest{
		Name:           name,
		WorkspaceScope: namedWorkspaceScope(workspace),
	})
	if err != nil {
		return nil, converter.FromGRPCError(err)
	}
	return converter.SecretFromProto(resp.GetSecret()), nil
}

// List returns a lazy pager over the secrets of a workspace. The gateway
// accepts only a named workspace for secrets, so it refuses
// ListOptions.AllWorkspaces.
func (s *secretClient) List(workspace string, opts ...ListOptions) (*Pager[*Secret], error) {
	pageSize, err := listPageSize(opts)
	if err != nil {
		return nil, err
	}
	var pageToken, labelSelector string
	var allWorkspaces bool
	if len(opts) > 0 {
		pageToken = opts[0].PageToken
		labelSelector = opts[0].LabelSelector
		allWorkspaces = opts[0].AllWorkspaces
	}
	workspaceScope := namedWorkspaceScope(workspace)
	if allWorkspaces {
		workspaceScope = allWorkspacesScope()
	}
	return newPager(pageToken, func(ctx context.Context, pageToken string) (*Page[*Secret], error) {
		req := &pb.ListSecretsRequest{
			WorkspaceScope: workspaceScope, PageSize: pageSize, PageToken: pageToken,
			LabelSelector: labelSelector,
		}
		resp, err := s.client.ListSecrets(ctx, req)
		if err != nil {
			return nil, converter.FromGRPCError(err)
		}
		secrets := make([]*Secret, 0, len(resp.GetSecrets()))
		for _, protoSecret := range resp.GetSecrets() {
			secrets = append(secrets, converter.SecretFromProto(protoSecret))
		}
		return &Page[*Secret]{Items: secrets, NextPageToken: resp.GetNextPageToken()}, nil
	}), nil
}

func (s *secretClient) ListAll(ctx context.Context, workspace string, opts ...ListOptions) ([]*Secret, error) {
	pager, err := s.List(workspace, opts...)
	if err != nil {
		return nil, err
	}
	return pager.All(ctx)
}

func (s *secretClient) Update(ctx context.Context, workspace, name string, update *SecretUpdate) (*Secret, error) {
	if update == nil {
		return nil, &StatusError{Code: ErrorInvalidArgument, Message: "update must not be nil"}
	}
	req := converter.SecretUpdateToProto(name, update)
	req.WorkspaceScope = namedWorkspaceScope(workspace)
	resp, err := s.client.UpdateSecret(ctx, req)
	if err != nil {
		return nil, converter.FromGRPCError(err)
	}
	return converter.SecretFromProto(resp.GetSecret()), nil
}

func (s *secretClient) Delete(ctx context.Context, workspace, name string, opts ...DeleteOptions) (*DeletionResult, error) {
	resp, err := s.client.DeleteSecret(ctx, &pb.DeleteSecretRequest{
		AllowMissing:   allowMissing(opts),
		Name:           name,
		WorkspaceScope: namedWorkspaceScope(workspace),
	})
	if err != nil {
		return nil, converter.FromGRPCError(err)
	}
	return &DeletionResult{Outcome: DeletionOutcome(resp.GetOutcome())}, nil
}
