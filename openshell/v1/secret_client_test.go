// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"testing"

	dm "github.com/ai-openshell/sdk-go/proto/datamodelv1"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Mock server for workspace secrets ---

type mockSecretServer struct {
	pb.UnimplementedOpenShellServer
	mu sync.Mutex

	secrets map[string]*pb.Secret
	// listPageSize splits ListSecrets responses into pages when positive.
	listPageSize int

	createRequest *pb.CreateSecretRequest
	getRequest    *pb.GetSecretRequest
	listRequests  []*pb.ListSecretsRequest
	updateRequest *pb.UpdateSecretRequest
	deleteRequest *pb.DeleteSecretRequest

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
}

func newMockSecretServer() *mockSecretServer {
	return &mockSecretServer{secrets: make(map[string]*pb.Secret)}
}

func (s *mockSecretServer) CreateSecret(_ context.Context, req *pb.CreateSecretRequest) (*pb.SecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createRequest = proto.Clone(req).(*pb.CreateSecretRequest)
	if s.createErr != nil {
		return nil, s.createErr
	}
	secret := &pb.Secret{
		Metadata: &dm.ObjectMeta{
			Id:              "secret-id-" + req.GetName(),
			Name:            req.GetName(),
			Labels:          req.GetLabels(),
			ResourceVersion: 1,
			Workspace:       req.GetWorkspaceScope().GetWorkspace(),
		},
		Description:     req.GetDescription(),
		RefreshUse:      req.GetRefreshUse(),
		ValueUpdateTime: timestamppb.Now(),
	}
	s.secrets[req.GetName()] = secret
	return &pb.SecretResponse{Secret: proto.Clone(secret).(*pb.Secret)}, nil
}

func (s *mockSecretServer) GetSecret(_ context.Context, req *pb.GetSecretRequest) (*pb.SecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getRequest = proto.Clone(req).(*pb.GetSecretRequest)
	if s.getErr != nil {
		return nil, s.getErr
	}
	secret, ok := s.secrets[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %q not found", req.GetName())
	}
	return &pb.SecretResponse{Secret: proto.Clone(secret).(*pb.Secret)}, nil
}

func (s *mockSecretServer) ListSecrets(_ context.Context, req *pb.ListSecretsRequest) (*pb.ListSecretsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listRequests = append(s.listRequests, proto.Clone(req).(*pb.ListSecretsRequest))
	if s.listErr != nil {
		return nil, s.listErr
	}
	names := make([]string, 0, len(s.secrets))
	for name := range s.secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	start := 0
	if req.GetPageToken() != "" {
		offset, err := strconv.Atoi(req.GetPageToken())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid page token")
		}
		start = offset
	}
	end := len(names)
	next := ""
	if s.listPageSize > 0 && start+s.listPageSize < len(names) {
		end = start + s.listPageSize
		next = strconv.Itoa(end)
	}
	secrets := make([]*pb.Secret, 0, end-start)
	for _, name := range names[start:end] {
		secrets = append(secrets, proto.Clone(s.secrets[name]).(*pb.Secret))
	}
	return &pb.ListSecretsResponse{Secrets: secrets, NextPageToken: next}, nil
}

func (s *mockSecretServer) UpdateSecret(_ context.Context, req *pb.UpdateSecretRequest) (*pb.SecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateRequest = proto.Clone(req).(*pb.UpdateSecretRequest)
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	secret, ok := s.secrets[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %q not found", req.GetName())
	}
	if req.ExpectedResourceVersion != nil && req.GetExpectedResourceVersion() != secret.GetMetadata().GetResourceVersion() {
		return nil, status.Error(codes.Aborted, "resource version mismatch")
	}
	if req.Value != nil {
		secret.ValueUpdateTime = timestamppb.Now()
	}
	if req.Description != nil {
		secret.Description = req.GetDescription()
	}
	if req.GetLabels() != nil {
		secret.Metadata.Labels = req.GetLabels().GetLabels()
	}
	secret.Metadata.ResourceVersion++
	return &pb.SecretResponse{Secret: proto.Clone(secret).(*pb.Secret)}, nil
}

func (s *mockSecretServer) DeleteSecret(_ context.Context, req *pb.DeleteSecretRequest) (*pb.DeleteSecretResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteRequest = proto.Clone(req).(*pb.DeleteSecretRequest)
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	if _, ok := s.secrets[req.GetName()]; !ok {
		if !req.GetAllowMissing() {
			return nil, status.Errorf(codes.NotFound, "secret %q not found", req.GetName())
		}
		return &pb.DeleteSecretResponse{Outcome: pb.DeletionOutcome_DELETION_OUTCOME_ALREADY_ABSENT}, nil
	}
	delete(s.secrets, req.GetName())
	return &pb.DeleteSecretResponse{Outcome: pb.DeletionOutcome_DELETION_OUTCOME_COMPLETED}, nil
}

func (s *mockSecretServer) seed(name string, labels map[string]string) {
	s.secrets[name] = &pb.Secret{
		Metadata:        &dm.ObjectMeta{Name: name, Workspace: "team-a", Labels: labels, ResourceVersion: 3},
		Description:     "seeded",
		ValueUpdateTime: timestamppb.Now(),
	}
}

// --- Test setup ---

func setupSecretTest(t *testing.T, mock *mockSecretServer) (*secretClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	pb.RegisterOpenShellServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	return newSecretClient(conn), func() {
		_ = conn.Close()
		srv.Stop()
	}
}

func strPtr(s string) *string { return &s }

// --- Create tests ---

func TestSecretCreate(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Create(context.Background(), "team-a", &SecretCreate{
		Name:        "oauth-client",
		Description: "OAuth client secret",
		Value:       "s3cr3t",
		RefreshUse:  &SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
		Labels:      map[string]string{"team": "platform"},
	})

	require.NoError(t, err)
	require.NotNil(t, secret)
	assert.Equal(t, "secret-id-oauth-client", secret.ID)
	assert.Equal(t, "oauth-client", secret.Name)
	assert.Equal(t, "team-a", secret.Workspace)
	assert.Equal(t, uint64(1), secret.ResourceVersion)
	assert.Equal(t, "OAuth client secret", secret.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, secret.Labels)
	require.NotNil(t, secret.RefreshUse)
	assert.Equal(t, []string{"https://idp.example.com/token"}, secret.RefreshUse.AllowedEndpoints)
	assert.False(t, secret.ValueUpdatedAt.IsZero())

	mock.mu.Lock()
	defer mock.mu.Unlock()
	req := mock.createRequest
	require.NotNil(t, req)
	assert.Equal(t, "team-a", req.GetWorkspaceScope().GetWorkspace())
	assert.Equal(t, "oauth-client", req.GetName())
	assert.Equal(t, "OAuth client secret", req.GetDescription())
	assert.Equal(t, "s3cr3t", req.GetValue())
	assert.Equal(t, []string{"https://idp.example.com/token"}, req.GetRefreshUse().GetAllowedEndpoints())
	assert.Equal(t, map[string]string{"team": "platform"}, req.GetLabels())
	assert.Empty(t, req.GetRequestId())
}

func TestSecretCreate_RevealOnlyOmitsRefreshUse(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Create(context.Background(), "team-a", &SecretCreate{Name: "plain", Value: "v"})

	require.NoError(t, err)
	assert.Nil(t, secret.RefreshUse)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Nil(t, mock.createRequest.RefreshUse)
}

func TestSecretCreate_RejectsNilBeforeRPC(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	_, err := client.Create(context.Background(), "team-a", nil)

	require.Error(t, err)
	assert.True(t, IsInvalidArgument(err))
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Nil(t, mock.createRequest)
}

func TestSecretCreate_Error(t *testing.T) {
	mock := newMockSecretServer()
	mock.createErr = status.Error(codes.AlreadyExists, "secret already exists")
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Create(context.Background(), "team-a", &SecretCreate{Name: "dup", Value: "v"})

	assert.Nil(t, secret)
	require.Error(t, err)
	assert.True(t, IsAlreadyExists(err))
}

// --- Get tests ---

func TestSecretGet(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("oauth-client", map[string]string{"team": "platform"})
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Get(context.Background(), "team-a", "oauth-client")

	require.NoError(t, err)
	assert.Equal(t, "oauth-client", secret.Name)
	assert.Equal(t, "seeded", secret.Description)
	assert.Equal(t, uint64(3), secret.ResourceVersion)
	assert.Nil(t, secret.RefreshUse)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.NotNil(t, mock.getRequest)
	assert.Equal(t, "team-a", mock.getRequest.GetWorkspaceScope().GetWorkspace())
	assert.Equal(t, "oauth-client", mock.getRequest.GetName())
}

func TestSecretGet_NotFound(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Get(context.Background(), "team-a", "missing")

	assert.Nil(t, secret)
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

// --- List tests ---

func TestSecretListAll(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("a", nil)
	mock.seed("b", nil)
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secrets, err := client.ListAll(context.Background(), "team-a", ListOptions{
		PageSize:      10,
		LabelSelector: "team=platform",
	})

	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, "a", secrets[0].Name)
	assert.Equal(t, "b", secrets[1].Name)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.listRequests, 1)
	req := mock.listRequests[0]
	assert.Equal(t, "team-a", req.GetWorkspaceScope().GetWorkspace())
	assert.Equal(t, int32(10), req.GetPageSize())
	assert.Empty(t, req.GetPageToken())
	assert.Equal(t, "team=platform", req.GetLabelSelector())
}

func TestSecretList_PagerFollowsContinuationTokens(t *testing.T) {
	mock := newMockSecretServer()
	mock.listPageSize = 2
	for _, name := range []string{"a", "b", "c"} {
		mock.seed(name, nil)
	}
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	pager, err := client.List("team-a", ListOptions{PageSize: 2})
	require.NoError(t, err)

	first, err := pager.NextPage(context.Background())
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	assert.Equal(t, "2", first.NextPageToken)

	second, err := pager.NextPage(context.Background())
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	assert.Equal(t, "c", second.Items[0].Name)
	assert.Empty(t, second.NextPageToken)

	done, err := pager.NextPage(context.Background())
	require.NoError(t, err)
	assert.Nil(t, done)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.listRequests, 2)
	assert.Empty(t, mock.listRequests[0].GetPageToken())
	assert.Equal(t, "2", mock.listRequests[1].GetPageToken())
	assert.Equal(t, int32(2), mock.listRequests[1].GetPageSize())
}

func TestSecretList_ResumesFromPageToken(t *testing.T) {
	mock := newMockSecretServer()
	for _, name := range []string{"a", "b", "c"} {
		mock.seed(name, nil)
	}
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secrets, err := client.ListAll(context.Background(), "team-a", ListOptions{PageToken: "1"})

	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, "b", secrets[0].Name)
}

func TestSecretList_RejectsNegativePagination(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	_, err := client.ListAll(context.Background(), "team-a", ListOptions{PageSize: -1})

	require.Error(t, err)
	assert.True(t, IsInvalidArgument(err))
}

func TestSecretList_EmptyReturnsNonNilSlice(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secrets, err := client.ListAll(context.Background(), "team-a")

	require.NoError(t, err)
	assert.NotNil(t, secrets)
	assert.Empty(t, secrets)
}

func TestSecretList_Error(t *testing.T) {
	mock := newMockSecretServer()
	mock.listErr = status.Error(codes.PermissionDenied, "denied")
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secrets, err := client.ListAll(context.Background(), "team-a")

	assert.Nil(t, secrets)
	require.Error(t, err)
	assert.True(t, IsPermissionDenied(err))
}

// --- Update tests ---

func TestSecretUpdate_PresenceOnTheWire(t *testing.T) {
	tests := []struct {
		name   string
		update SecretUpdate
		check  func(t *testing.T, req *pb.UpdateSecretRequest)
	}{
		{
			name:   "empty update names nothing",
			update: SecretUpdate{},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				assert.Nil(t, req.Value)
				assert.Nil(t, req.Description)
				assert.Nil(t, req.Labels)
				assert.Nil(t, req.ExpectedResourceVersion)
			},
		},
		{
			name:   "value only",
			update: SecretUpdate{Value: strPtr("rotated")},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				require.NotNil(t, req.Value)
				assert.Equal(t, "rotated", req.GetValue())
				assert.Nil(t, req.Description)
				assert.Nil(t, req.Labels)
				assert.Nil(t, req.ExpectedResourceVersion)
			},
		},
		{
			name:   "description only",
			update: SecretUpdate{Description: strPtr("new text")},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				assert.Nil(t, req.Value)
				require.NotNil(t, req.Description)
				assert.Equal(t, "new text", req.GetDescription())
				assert.Nil(t, req.Labels)
			},
		},
		{
			name:   "empty description is present",
			update: SecretUpdate{Description: strPtr("")},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				assert.Nil(t, req.Value)
				require.NotNil(t, req.Description)
				assert.Empty(t, req.GetDescription())
			},
		},
		{
			name:   "labels replaced with a set",
			update: SecretUpdate{Labels: &SecretLabels{Labels: map[string]string{"env": "prod"}}},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				assert.Nil(t, req.Value)
				assert.Nil(t, req.Description)
				require.NotNil(t, req.Labels)
				assert.Equal(t, map[string]string{"env": "prod"}, req.GetLabels().GetLabels())
			},
		},
		{
			name:   "labels replaced with an empty set",
			update: SecretUpdate{Labels: &SecretLabels{Labels: map[string]string{}}},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				require.NotNil(t, req.Labels)
				assert.Empty(t, req.GetLabels().GetLabels())
			},
		},
		{
			name:   "labels replaced with a nil set",
			update: SecretUpdate{Labels: &SecretLabels{}},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				require.NotNil(t, req.Labels)
				assert.Empty(t, req.GetLabels().GetLabels())
			},
		},
		{
			name:   "expected resource version",
			update: SecretUpdate{Description: strPtr("d"), ExpectedResourceVersion: 3},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				require.NotNil(t, req.ExpectedResourceVersion)
				assert.Equal(t, uint64(3), req.GetExpectedResourceVersion())
			},
		},
		{
			name: "everything",
			update: SecretUpdate{
				Value:                   strPtr("v2"),
				Description:             strPtr("d2"),
				Labels:                  &SecretLabels{Labels: map[string]string{"a": "b"}},
				ExpectedResourceVersion: 3,
			},
			check: func(t *testing.T, req *pb.UpdateSecretRequest) {
				assert.Equal(t, "v2", req.GetValue())
				assert.Equal(t, "d2", req.GetDescription())
				assert.Equal(t, map[string]string{"a": "b"}, req.GetLabels().GetLabels())
				assert.Equal(t, uint64(3), req.GetExpectedResourceVersion())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockSecretServer()
			mock.seed("oauth-client", map[string]string{"team": "platform"})
			client, cleanup := setupSecretTest(t, mock)
			defer cleanup()

			update := tt.update
			secret, err := client.Update(context.Background(), "team-a", "oauth-client", &update)

			require.NoError(t, err)
			require.NotNil(t, secret)
			assert.Equal(t, uint64(4), secret.ResourceVersion)

			mock.mu.Lock()
			defer mock.mu.Unlock()
			req := mock.updateRequest
			require.NotNil(t, req)
			assert.Equal(t, "team-a", req.GetWorkspaceScope().GetWorkspace())
			assert.Equal(t, "oauth-client", req.GetName())
			assert.Empty(t, req.GetRequestId())
			tt.check(t, req)
		})
	}
}

func TestSecretUpdate_AbsentFieldsLeaveTheSecretUnchanged(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("oauth-client", map[string]string{"team": "platform"})
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Update(context.Background(), "team-a", "oauth-client", &SecretUpdate{Value: strPtr("rotated")})

	require.NoError(t, err)
	assert.Equal(t, "seeded", secret.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, secret.Labels)
}

func TestSecretUpdate_EmptyLabelSetRemovesLabels(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("oauth-client", map[string]string{"team": "platform"})
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Update(context.Background(), "team-a", "oauth-client", &SecretUpdate{Labels: &SecretLabels{}})

	require.NoError(t, err)
	assert.Empty(t, secret.Labels)
	assert.Equal(t, "seeded", secret.Description)
}

func TestSecretUpdate_ResourceVersionMismatchIsConflict(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("oauth-client", nil)
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Update(context.Background(), "team-a", "oauth-client", &SecretUpdate{
		Description:             strPtr("d"),
		ExpectedResourceVersion: 2,
	})

	assert.Nil(t, secret)
	require.Error(t, err)
	assert.True(t, IsConflict(err))
}

func TestSecretUpdate_RejectsNilBeforeRPC(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	_, err := client.Update(context.Background(), "team-a", "oauth-client", nil)

	require.Error(t, err)
	assert.True(t, IsInvalidArgument(err))
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Nil(t, mock.updateRequest)
}

func TestSecretUpdate_NotFound(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	secret, err := client.Update(context.Background(), "team-a", "missing", &SecretUpdate{Value: strPtr("v")})

	assert.Nil(t, secret)
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

// --- Delete tests ---

func TestSecretDelete(t *testing.T) {
	mock := newMockSecretServer()
	mock.seed("oauth-client", nil)
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	deleted, err := client.Delete(context.Background(), "team-a", "oauth-client")

	require.NoError(t, err)
	assert.Equal(t, DeletionCompleted, deleted.Outcome)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.NotNil(t, mock.deleteRequest)
	assert.Equal(t, "team-a", mock.deleteRequest.GetWorkspaceScope().GetWorkspace())
	assert.Equal(t, "oauth-client", mock.deleteRequest.GetName())
	assert.False(t, mock.deleteRequest.GetAllowMissing())
	assert.Empty(t, mock.deleteRequest.GetRequestId())
}

func TestSecretDelete_Missing(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	deleted, err := client.Delete(context.Background(), "team-a", "missing")

	assert.Nil(t, deleted)
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

func TestSecretDelete_AllowMissing(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()

	deleted, err := client.Delete(context.Background(), "team-a", "missing", DeleteOptions{AllowMissing: true})

	require.NoError(t, err)
	assert.Equal(t, DeletionAlreadyAbsent, deleted.Outcome)
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.True(t, mock.deleteRequest.GetAllowMissing())
}

// --- Integration test: full lifecycle ---

func TestSecretLifecycle(t *testing.T) {
	mock := newMockSecretServer()
	client, cleanup := setupSecretTest(t, mock)
	defer cleanup()
	ctx := context.Background()

	created, err := client.Create(ctx, "team-a", &SecretCreate{
		Name:   "oauth-client",
		Value:  "v1",
		Labels: map[string]string{"team": "platform"},
	})
	require.NoError(t, err)

	updated, err := client.Update(ctx, "team-a", "oauth-client", &SecretUpdate{
		Value:                   strPtr("v2"),
		ExpectedResourceVersion: created.ResourceVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, created.ResourceVersion+1, updated.ResourceVersion)
	assert.Equal(t, map[string]string{"team": "platform"}, updated.Labels)

	got, err := client.Get(ctx, "team-a", "oauth-client")
	require.NoError(t, err)
	assert.Equal(t, updated.ResourceVersion, got.ResourceVersion)

	deleted, err := client.Delete(ctx, "team-a", "oauth-client")
	require.NoError(t, err)
	assert.Equal(t, DeletionCompleted, deleted.Outcome)

	_, err = client.Get(ctx, "team-a", "oauth-client")
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}
