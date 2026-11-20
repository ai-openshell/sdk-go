// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"slices"
	"strings"
	"time"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// secretName extracts the name from a Secret pointer for use as the
// objectStore key function.
func secretName(s *types.Secret) string {
	return s.Name
}

// copySecret returns a deep copy of a Secret pointer. All maps and slices are
// duplicated to prevent aliasing.
func copySecret(s *types.Secret) *types.Secret {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Labels = copyStringMap(s.Labels)
	cp.Annotations = copyStringMap(s.Annotations)
	if s.DeletionTimestamp != nil {
		t := *s.DeletionTimestamp
		cp.DeletionTimestamp = &t
	}
	cp.RefreshUse = copySecretRefreshUse(s.RefreshUse)
	return &cp
}

func copySecretRefreshUse(u *types.SecretRefreshUse) *types.SecretRefreshUse {
	if u == nil {
		return nil
	}
	return &types.SecretRefreshUse{AllowedEndpoints: copyStringSlice(u.AllowedEndpoints)}
}

// fakeSecretClient implements v1.SecretInterface backed by an in-memory
// objectStore. Like the gateway, it keeps secret values write-only: a value is
// validated and then discarded, and no method returns one.
type fakeSecretClient struct {
	store      *objectStore[*types.Secret]
	closedFunc func() bool
}

var _ v1.SecretInterface = (*fakeSecretClient)(nil)

// newFakeSecretClient creates a new fakeSecretClient.
func newFakeSecretClient(
	store *objectStore[*types.Secret],
	closedFunc func() bool,
) *fakeSecretClient {
	return &fakeSecretClient{
		store:      store,
		closedFunc: closedFunc,
	}
}

// Create adds a new secret. CreatedAt, ValueUpdatedAt and ResourceVersion are
// set automatically.
func (c *fakeSecretClient) Create(_ context.Context, workspace string, secret *types.SecretCreate) (*types.Secret, error) {
	if c.closedFunc() {
		return nil, &types.StatusError{Code: types.ErrorUnavailable, Message: "client is closed"}
	}
	if secret == nil {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "secret must not be nil"}
	}
	if secret.Name == "" {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "secret name is required"}
	}
	if secret.Value == "" {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "secret value must not be empty"}
	}
	if secret.RefreshUse != nil && len(secret.RefreshUse.AllowedEndpoints) == 0 {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "refresh use allowed endpoints must not be empty"}
	}

	now := time.Now()
	return c.store.Create(workspace, &types.Secret{
		Name:            secret.Name,
		CreatedAt:       now,
		Labels:          copyStringMap(secret.Labels),
		ResourceVersion: 1,
		Workspace:       workspace,
		Description:     secret.Description,
		RefreshUse:      copySecretRefreshUse(secret.RefreshUse),
		ValueUpdatedAt:  now,
	})
}

// Get retrieves secret metadata by name.
func (c *fakeSecretClient) Get(_ context.Context, workspace, name string) (*types.Secret, error) {
	if c.closedFunc() {
		return nil, &types.StatusError{Code: types.ErrorUnavailable, Message: "client is closed"}
	}
	return c.store.Get(workspace, name)
}

// List returns a lazy pager over the secrets of a workspace. Like the gateway,
// it accepts only a named workspace.
func (c *fakeSecretClient) List(workspace string, opts ...v1.ListOptions) (*v1.Pager[*types.Secret], error) {
	if c.closedFunc() {
		return nil, &types.StatusError{Code: types.ErrorUnavailable, Message: "client is closed"}
	}
	var options v1.ListOptions
	if len(opts) > 0 {
		options = opts[0]
		if options.PageSize < 0 {
			return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "page size must not be negative"}
		}
	}
	if options.AllWorkspaces {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "secrets can only be listed in a named workspace"}
	}
	secrets := c.store.List(workspace)
	slices.SortFunc(secrets, compareSecretsForList)
	secrets, err := filterSecretsByLabelSelector(secrets, options.LabelSelector)
	if err != nil {
		return nil, err
	}
	return newSlicePager(secrets, options.PageSize, options.PageToken)
}

func (c *fakeSecretClient) ListAll(ctx context.Context, workspace string, opts ...v1.ListOptions) ([]*types.Secret, error) {
	pager, err := c.List(workspace, opts...)
	if err != nil {
		return nil, err
	}
	return pager.All(ctx)
}

// Update applies the fields the update names and leaves the rest unchanged.
// ResourceVersion is incremented automatically, and ValueUpdatedAt moves only
// when the update carries a value.
func (c *fakeSecretClient) Update(_ context.Context, workspace, name string, update *types.SecretUpdate) (*types.Secret, error) {
	if c.closedFunc() {
		return nil, &types.StatusError{Code: types.ErrorUnavailable, Message: "client is closed"}
	}
	if update == nil {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "update must not be nil"}
	}
	if update.Value != nil && *update.Value == "" {
		return nil, &types.StatusError{Code: types.ErrorInvalidArgument, Message: "secret value must not be empty"}
	}

	s, err := c.store.Get(workspace, name)
	if err != nil {
		return nil, err
	}
	if update.ExpectedResourceVersion != 0 && update.ExpectedResourceVersion != s.ResourceVersion {
		return nil, &types.StatusError{Code: types.ErrorConflict, Message: "secret resource version does not match"}
	}

	if update.Value != nil {
		s.ValueUpdatedAt = time.Now()
	}
	if update.Description != nil {
		s.Description = *update.Description
	}
	if update.Labels != nil {
		s.Labels = copyStringMap(update.Labels.Labels)
	}
	s.ResourceVersion++

	return c.store.Update(workspace, s)
}

// Delete removes a secret by name.
func (c *fakeSecretClient) Delete(_ context.Context, workspace, name string, opts ...v1.DeleteOptions) (*types.DeletionResult, error) {
	if c.closedFunc() {
		return nil, &types.StatusError{Code: types.ErrorUnavailable, Message: "client is closed"}
	}
	_, existed := c.store.DeleteAndGet(workspace, name)
	return deletionResult(existed, "", opts)
}

func filterSecretsByLabelSelector(secrets []*types.Secret, selector string) ([]*types.Secret, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return secrets, nil
	}
	labels, err := parseSimpleLabelSelector(selector)
	if err != nil {
		return nil, err
	}
	filtered := make([]*types.Secret, 0, len(secrets))
	for _, secret := range secrets {
		if labelsMatchSelector(secret.Labels, labels) {
			filtered = append(filtered, secret)
		}
	}
	return filtered, nil
}

func compareSecretsForList(a, b *types.Secret) int {
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}
