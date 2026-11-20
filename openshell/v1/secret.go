// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"

	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// Secret is the metadata of a workspace secret. It never carries the value.
type Secret = types.Secret

// SecretRefreshUse allows provider refresh to use a secret's value.
type SecretRefreshUse = types.SecretRefreshUse

// SecretCreate describes a workspace secret to create.
type SecretCreate = types.SecretCreate

// SecretLabels is a complete label set for a workspace secret.
type SecretLabels = types.SecretLabels

// SecretUpdate describes changes to a workspace secret.
type SecretUpdate = types.SecretUpdate

// SecretInterface defines CRUD operations on workspace secrets.
//
// Secret values are write-only: Create and Update send one, and no method
// returns one.
type SecretInterface interface {
	Create(ctx context.Context, workspace string, secret *SecretCreate) (*Secret, error)
	Get(ctx context.Context, workspace, name string) (*Secret, error)
	List(workspace string, opts ...ListOptions) (*Pager[*Secret], error)
	ListAll(ctx context.Context, workspace string, opts ...ListOptions) ([]*Secret, error)
	Update(ctx context.Context, workspace, name string, update *SecretUpdate) (*Secret, error)
	Delete(ctx context.Context, workspace, name string, opts ...DeleteOptions) (*DeletionResult, error)
}
