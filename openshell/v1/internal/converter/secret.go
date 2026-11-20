// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converter

import (
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	pb "github.com/ai-openshell/sdk-go/proto/openshellv1"
)

// SecretFromProto converts a proto Secret to an SDK Secret. Labels,
// annotations and allowed endpoints are deep-copied.
func SecretFromProto(s *pb.Secret) *types.Secret {
	if s == nil {
		return nil
	}

	result := &types.Secret{
		Description:    s.GetDescription(),
		RefreshUse:     SecretRefreshUseFromProto(s.GetRefreshUse()),
		ValueUpdatedAt: TimeFromProto(s.GetValueUpdateTime()),
	}

	if m := s.GetMetadata(); m != nil {
		result.ID = m.GetId()
		result.Name = m.GetName()
		result.CreatedAt = TimeFromProto(m.GetCreatedTime())
		result.Labels = CopyStringMap(m.GetLabels())
		result.Annotations = CopyStringMap(m.GetAnnotations())
		result.ResourceVersion = m.GetResourceVersion()
		result.Workspace = m.GetWorkspace()
		result.DeletionTimestamp = TimePtrFromProto(m.GetDeletionTime())
	}

	return result
}

// SecretRefreshUseFromProto converts a proto SecretRefreshUse to an SDK
// SecretRefreshUse. Presence is preserved: nil stays nil.
func SecretRefreshUseFromProto(u *pb.SecretRefreshUse) *types.SecretRefreshUse {
	if u == nil {
		return nil
	}
	return &types.SecretRefreshUse{AllowedEndpoints: CopyStringSlice(u.GetAllowedEndpoints())}
}

// SecretRefreshUseToProto converts an SDK SecretRefreshUse to a proto
// SecretRefreshUse. Presence is preserved: nil stays nil.
func SecretRefreshUseToProto(u *types.SecretRefreshUse) *pb.SecretRefreshUse {
	if u == nil {
		return nil
	}
	return &pb.SecretRefreshUse{AllowedEndpoints: CopyStringSlice(u.AllowedEndpoints)}
}

// SecretCreateToProto converts an SDK SecretCreate to a proto
// CreateSecretRequest without a workspace scope. Labels and allowed endpoints
// are deep-copied.
func SecretCreateToProto(c *types.SecretCreate) *pb.CreateSecretRequest {
	if c == nil {
		return nil
	}
	return &pb.CreateSecretRequest{
		Name:        c.Name,
		Description: c.Description,
		Value:       c.Value,
		RefreshUse:  SecretRefreshUseToProto(c.RefreshUse),
		Labels:      CopyStringMap(c.Labels),
	}
}

// SecretUpdateToProto converts an SDK SecretUpdate to a proto
// UpdateSecretRequest for the named secret, without a workspace scope. A nil
// field stays absent so that the gateway leaves it unchanged; labels are
// deep-copied.
func SecretUpdateToProto(name string, u *types.SecretUpdate) *pb.UpdateSecretRequest {
	if u == nil {
		return nil
	}

	result := &pb.UpdateSecretRequest{Name: name}

	if u.Value != nil {
		value := *u.Value
		result.Value = &value
	}
	if u.Description != nil {
		description := *u.Description
		result.Description = &description
	}
	if u.Labels != nil {
		result.Labels = &pb.SecretLabels{Labels: CopyStringMap(u.Labels.Labels)}
	}
	if u.ExpectedResourceVersion != 0 {
		version := u.ExpectedResourceVersion
		result.ExpectedResourceVersion = &version
	}

	return result
}
