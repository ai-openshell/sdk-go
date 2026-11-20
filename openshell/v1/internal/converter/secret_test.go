// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converter

import (
	"testing"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1/types"
	dm "github.com/ai-openshell/sdk-go/proto/datamodelv1"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SecretFromProto ---

func TestSecretFromProto_Nil(t *testing.T) {
	assert.Nil(t, SecretFromProto(nil))
}

func TestSecretFromProto_Full(t *testing.T) {
	proto := &pb.Secret{
		Metadata: &dm.ObjectMeta{
			Id:              "sec-1",
			Name:            "oauth-client",
			CreatedTime:     TimestampFromMillis(1700000000000),
			Labels:          map[string]string{"team": "platform"},
			Annotations:     map[string]string{"owner": "alice"},
			ResourceVersion: 7,
			Workspace:       "team-a",
			DeletionTime:    TimestampFromMillis(1700000300000),
		},
		Description:     "OAuth client secret",
		RefreshUse:      &pb.SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
		ValueUpdateTime: TimestampFromMillis(1700000100000),
	}

	secret := SecretFromProto(proto)

	require.NotNil(t, secret)
	assert.Equal(t, "sec-1", secret.ID)
	assert.Equal(t, "oauth-client", secret.Name)
	assert.Equal(t, TimeFromMillis(1700000000000), secret.CreatedAt)
	assert.Equal(t, map[string]string{"team": "platform"}, secret.Labels)
	assert.Equal(t, map[string]string{"owner": "alice"}, secret.Annotations)
	assert.Equal(t, uint64(7), secret.ResourceVersion)
	assert.Equal(t, "team-a", secret.Workspace)
	require.NotNil(t, secret.DeletionTimestamp)
	assert.Equal(t, TimeFromMillis(1700000300000), *secret.DeletionTimestamp)
	assert.Equal(t, "OAuth client secret", secret.Description)
	require.NotNil(t, secret.RefreshUse)
	assert.Equal(t, []string{"https://idp.example.com/token"}, secret.RefreshUse.AllowedEndpoints)
	assert.Equal(t, TimeFromMillis(1700000100000), secret.ValueUpdatedAt)
}

func TestSecretFromProto_DeepCopy(t *testing.T) {
	proto := &pb.Secret{
		Metadata: &dm.ObjectMeta{
			Name:        "oauth-client",
			Labels:      map[string]string{"team": "platform"},
			Annotations: map[string]string{"owner": "alice"},
		},
		RefreshUse: &pb.SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
	}

	secret := SecretFromProto(proto)
	proto.Metadata.Labels["team"] = "mutated"
	proto.Metadata.Annotations["owner"] = "mutated"
	proto.RefreshUse.AllowedEndpoints[0] = "mutated"

	assert.Equal(t, "platform", secret.Labels["team"])
	assert.Equal(t, "alice", secret.Annotations["owner"])
	assert.Equal(t, "https://idp.example.com/token", secret.RefreshUse.AllowedEndpoints[0])
}

func TestSecretFromProto_NilMetadata(t *testing.T) {
	secret := SecretFromProto(&pb.Secret{Description: "d"})

	require.NotNil(t, secret)
	assert.Empty(t, secret.Name)
	assert.Equal(t, "d", secret.Description)
	assert.Nil(t, secret.DeletionTimestamp)
	assert.True(t, secret.ValueUpdatedAt.IsZero())
}

func TestSecretFromProto_RevealOnlyHasNoRefreshUse(t *testing.T) {
	secret := SecretFromProto(&pb.Secret{Metadata: &dm.ObjectMeta{Name: "plain"}})

	require.NotNil(t, secret)
	assert.Nil(t, secret.RefreshUse)
}

// --- SecretRefreshUse ---

func TestSecretRefreshUse_NilStaysNil(t *testing.T) {
	assert.Nil(t, SecretRefreshUseFromProto(nil))
	assert.Nil(t, SecretRefreshUseToProto(nil))
}

func TestSecretRefreshUse_EmptyStaysPresent(t *testing.T) {
	fromProto := SecretRefreshUseFromProto(&pb.SecretRefreshUse{})
	require.NotNil(t, fromProto)
	assert.Empty(t, fromProto.AllowedEndpoints)

	toProto := SecretRefreshUseToProto(&v1.SecretRefreshUse{})
	require.NotNil(t, toProto)
	assert.Empty(t, toProto.AllowedEndpoints)
}

// --- SecretCreateToProto ---

func TestSecretCreateToProto_Nil(t *testing.T) {
	assert.Nil(t, SecretCreateToProto(nil))
}

func TestSecretCreateToProto_Full(t *testing.T) {
	create := &v1.SecretCreate{
		Name:        "oauth-client",
		Description: "OAuth client secret",
		Value:       "s3cr3t",
		RefreshUse:  &v1.SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
		Labels:      map[string]string{"team": "platform"},
	}

	proto := SecretCreateToProto(create)

	require.NotNil(t, proto)
	assert.Nil(t, proto.WorkspaceScope)
	assert.Equal(t, "oauth-client", proto.Name)
	assert.Equal(t, "OAuth client secret", proto.Description)
	assert.Equal(t, "s3cr3t", proto.Value)
	assert.Equal(t, []string{"https://idp.example.com/token"}, proto.RefreshUse.AllowedEndpoints)
	assert.Equal(t, map[string]string{"team": "platform"}, proto.Labels)
	assert.Empty(t, proto.RequestId)

	// Verify deep copy by mutating the original
	create.Labels["team"] = "mutated"
	create.RefreshUse.AllowedEndpoints[0] = "mutated"
	assert.Equal(t, "platform", proto.Labels["team"], "labels must be deep copied")
	assert.Equal(t, "https://idp.example.com/token", proto.RefreshUse.AllowedEndpoints[0], "allowed endpoints must be deep copied")
}

func TestSecretCreateToProto_RevealOnlyOmitsRefreshUse(t *testing.T) {
	proto := SecretCreateToProto(&v1.SecretCreate{Name: "plain", Value: "v"})

	require.NotNil(t, proto)
	assert.Nil(t, proto.RefreshUse)
	assert.Nil(t, proto.Labels)
}

// --- SecretUpdateToProto ---

func TestSecretUpdateToProto_Nil(t *testing.T) {
	assert.Nil(t, SecretUpdateToProto("oauth-client", nil))
}

func TestSecretUpdateToProto_EmptyNamesNothing(t *testing.T) {
	proto := SecretUpdateToProto("oauth-client", &v1.SecretUpdate{})

	require.NotNil(t, proto)
	assert.Equal(t, "oauth-client", proto.Name)
	assert.Nil(t, proto.WorkspaceScope)
	assert.Nil(t, proto.Value)
	assert.Nil(t, proto.Description)
	assert.Nil(t, proto.Labels)
	assert.Nil(t, proto.ExpectedResourceVersion)
	assert.Empty(t, proto.RequestId)
}

func TestSecretUpdateToProto_Value(t *testing.T) {
	value := "rotated"
	proto := SecretUpdateToProto("oauth-client", &v1.SecretUpdate{Value: &value})

	require.NotNil(t, proto.Value)
	assert.Equal(t, "rotated", *proto.Value)
	assert.NotSame(t, &value, proto.Value)
	assert.Nil(t, proto.Description)
	assert.Nil(t, proto.Labels)
}

func TestSecretUpdateToProto_EmptyDescriptionIsPresent(t *testing.T) {
	description := ""
	proto := SecretUpdateToProto("oauth-client", &v1.SecretUpdate{Description: &description})

	require.NotNil(t, proto.Description)
	assert.Empty(t, *proto.Description)
	assert.Nil(t, proto.Value)
	assert.Nil(t, proto.Labels)
}

func TestSecretUpdateToProto_Labels(t *testing.T) {
	tests := []struct {
		name   string
		labels *v1.SecretLabels
		want   *pb.SecretLabels
	}{
		{name: "absent leaves labels unchanged", labels: nil, want: nil},
		{name: "nil set replaces with nothing", labels: &v1.SecretLabels{}, want: &pb.SecretLabels{}},
		{name: "empty set replaces with nothing", labels: &v1.SecretLabels{Labels: map[string]string{}}, want: &pb.SecretLabels{Labels: map[string]string{}}},
		{name: "set replaces", labels: &v1.SecretLabels{Labels: map[string]string{"env": "prod"}}, want: &pb.SecretLabels{Labels: map[string]string{"env": "prod"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto := SecretUpdateToProto("oauth-client", &v1.SecretUpdate{Labels: tt.labels})

			if tt.want == nil {
				assert.Nil(t, proto.Labels)
				return
			}
			require.NotNil(t, proto.Labels)
			assert.Equal(t, tt.want.GetLabels(), proto.Labels.GetLabels())
		})
	}
}

func TestSecretUpdateToProto_LabelsDeepCopy(t *testing.T) {
	update := &v1.SecretUpdate{Labels: &v1.SecretLabels{Labels: map[string]string{"env": "prod"}}}

	proto := SecretUpdateToProto("oauth-client", update)
	update.Labels.Labels["env"] = "mutated"

	assert.Equal(t, "prod", proto.Labels.Labels["env"], "labels must be deep copied")
}

func TestSecretUpdateToProto_ExpectedResourceVersion(t *testing.T) {
	proto := SecretUpdateToProto("oauth-client", &v1.SecretUpdate{ExpectedResourceVersion: 7})

	require.NotNil(t, proto.ExpectedResourceVersion)
	assert.Equal(t, uint64(7), *proto.ExpectedResourceVersion)
}
