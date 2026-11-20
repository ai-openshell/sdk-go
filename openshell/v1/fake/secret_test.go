// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"testing"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSecretClient() *fakeSecretClient {
	store := newobjectStore(secretName, copySecret)
	return newFakeSecretClient(store, func() bool { return false })
}

func testSecretCreate(name string) *types.SecretCreate {
	return &types.SecretCreate{Name: name, Value: "s3cr3t"}
}

func secretStringPtr(s string) *string { return &s }

func TestSecret_CreateGetListDelete(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	created, err := sc.Create(ctx, "team-a", &types.SecretCreate{
		Name:        "oauth-client",
		Description: "OAuth client secret",
		Value:       "s3cr3t",
		RefreshUse:  &types.SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
		Labels:      map[string]string{"team": "platform"},
	})
	require.NoError(t, err)
	assert.Equal(t, "oauth-client", created.Name)
	assert.Equal(t, "team-a", created.Workspace)
	assert.Equal(t, uint64(1), created.ResourceVersion)
	assert.Equal(t, "OAuth client secret", created.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, created.Labels)
	require.NotNil(t, created.RefreshUse)
	assert.Equal(t, []string{"https://idp.example.com/token"}, created.RefreshUse.AllowedEndpoints)
	assert.False(t, created.CreatedAt.IsZero())
	assert.Equal(t, created.CreatedAt, created.ValueUpdatedAt)

	got, err := sc.Get(ctx, "team-a", "oauth-client")
	require.NoError(t, err)
	assert.Equal(t, created, got)

	list, err := sc.ListAll(ctx, "team-a")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "oauth-client", list[0].Name)

	deleted, err := sc.Delete(ctx, "team-a", "oauth-client")
	require.NoError(t, err)
	assert.Equal(t, types.DeletionCompleted, deleted.Outcome)

	_, err = sc.Get(ctx, "team-a", "oauth-client")
	require.Error(t, err)
	assert.True(t, types.IsNotFound(err))
}

func TestSecret_CreateAlreadyExists(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	_, err := sc.Create(ctx, "team-a", testSecretCreate("dup"))
	require.NoError(t, err)

	_, err = sc.Create(ctx, "team-a", testSecretCreate("dup"))
	require.Error(t, err)
	assert.True(t, types.IsAlreadyExists(err))
}

func TestSecret_CreateRejectsInvalidInput(t *testing.T) {
	tests := map[string]*types.SecretCreate{
		"nil":                         nil,
		"missing name":                {Value: "v"},
		"empty value":                 {Name: "s"},
		"refresh use with no targets": {Name: "s", Value: "v", RefreshUse: &types.SecretRefreshUse{}},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			sc := newTestSecretClient()

			_, err := sc.Create(context.Background(), "team-a", create)

			require.Error(t, err)
			assert.True(t, types.IsInvalidArgument(err))
		})
	}
}

func TestSecret_ListIsWorkspaceScoped(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	_, err := sc.Create(ctx, "team-a", testSecretCreate("a"))
	require.NoError(t, err)
	_, err = sc.Create(ctx, "team-b", testSecretCreate("b"))
	require.NoError(t, err)

	list, err := sc.ListAll(ctx, "team-a")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "a", list[0].Name)
}

func TestSecret_ListRejectsAllWorkspaces(t *testing.T) {
	sc := newTestSecretClient()

	_, err := sc.List("team-a", v1.ListOptions{AllWorkspaces: true})

	require.Error(t, err)
	assert.True(t, types.IsInvalidArgument(err))
}

func TestSecret_ListFiltersByLabelSelector(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	for name, team := range map[string]string{"a": "platform", "b": "runtime"} {
		create := testSecretCreate(name)
		create.Labels = map[string]string{"team": team}
		_, err := sc.Create(ctx, "team-a", create)
		require.NoError(t, err)
	}

	list, err := sc.ListAll(ctx, "team-a", v1.ListOptions{LabelSelector: "team=runtime"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "b", list[0].Name)

	_, err = sc.List("team-a", v1.ListOptions{LabelSelector: "not-a-pair"})
	require.Error(t, err)
	assert.True(t, types.IsInvalidArgument(err))
}

func TestSecret_ListPaginates(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c"} {
		_, err := sc.Create(ctx, "team-a", testSecretCreate(name))
		require.NoError(t, err)
	}

	pager, err := sc.List("team-a", v1.ListOptions{PageSize: 2})
	require.NoError(t, err)
	first, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.Len(t, first.Items, 2)
	assert.NotEmpty(t, first.NextPageToken)
	second, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.Len(t, second.Items, 1)
	assert.Empty(t, second.NextPageToken)
}

func TestSecret_ListRejectsNegativePagination(t *testing.T) {
	sc := newTestSecretClient()

	_, err := sc.List("team-a", v1.ListOptions{PageSize: -1})

	require.Error(t, err)
	assert.True(t, types.IsInvalidArgument(err))
}

func TestSecret_UpdateAppliesOnlyWhatIsNamed(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	create := testSecretCreate("oauth-client")
	create.Description = "original"
	create.Labels = map[string]string{"team": "platform"}
	created, err := sc.Create(ctx, "team-a", create)
	require.NoError(t, err)

	// Nothing named: only the resource version moves.
	updated, err := sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{})
	require.NoError(t, err)
	assert.Equal(t, uint64(2), updated.ResourceVersion)
	assert.Equal(t, "original", updated.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, updated.Labels)
	assert.Equal(t, created.ValueUpdatedAt, updated.ValueUpdatedAt)

	// Description only.
	updated, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{Description: secretStringPtr("changed")})
	require.NoError(t, err)
	assert.Equal(t, "changed", updated.Description)
	assert.Equal(t, map[string]string{"team": "platform"}, updated.Labels)
	assert.Equal(t, created.ValueUpdatedAt, updated.ValueUpdatedAt)

	// An empty description is present and clears it.
	updated, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{Description: secretStringPtr("")})
	require.NoError(t, err)
	assert.Empty(t, updated.Description)

	// Labels replace the whole set.
	updated, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{
		Labels: &types.SecretLabels{Labels: map[string]string{"env": "prod"}},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"env": "prod"}, updated.Labels)

	// A present, empty label set removes every label.
	updated, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{Labels: &types.SecretLabels{}})
	require.NoError(t, err)
	assert.Empty(t, updated.Labels)

	// A value moves ValueUpdatedAt.
	updated, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{Value: secretStringPtr("rotated")})
	require.NoError(t, err)
	assert.False(t, updated.ValueUpdatedAt.Before(created.ValueUpdatedAt))
	assert.Equal(t, uint64(7), updated.ResourceVersion)
	require.Nil(t, updated.RefreshUse)
}

func TestSecret_UpdateRejectsEmptyValue(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()
	_, err := sc.Create(ctx, "team-a", testSecretCreate("oauth-client"))
	require.NoError(t, err)

	_, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{Value: secretStringPtr("")})

	require.Error(t, err)
	assert.True(t, types.IsInvalidArgument(err))
}

func TestSecret_UpdateChecksExpectedResourceVersion(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()
	created, err := sc.Create(ctx, "team-a", testSecretCreate("oauth-client"))
	require.NoError(t, err)

	_, err = sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{
		Description:             secretStringPtr("d"),
		ExpectedResourceVersion: created.ResourceVersion + 1,
	})
	require.Error(t, err)
	assert.True(t, types.IsConflict(err))

	updated, err := sc.Update(ctx, "team-a", "oauth-client", &types.SecretUpdate{
		Description:             secretStringPtr("d"),
		ExpectedResourceVersion: created.ResourceVersion,
	})
	require.NoError(t, err)
	assert.Equal(t, "d", updated.Description)
}

func TestSecret_UpdateNilAndNotFound(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	_, err := sc.Update(ctx, "team-a", "oauth-client", nil)
	require.Error(t, err)
	assert.True(t, types.IsInvalidArgument(err))

	_, err = sc.Update(ctx, "team-a", "missing", &types.SecretUpdate{Description: secretStringPtr("d")})
	require.Error(t, err)
	assert.True(t, types.IsNotFound(err))
}

func TestSecret_DeleteMissing(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	_, err := sc.Delete(ctx, "team-a", "missing")
	require.Error(t, err)
	assert.True(t, types.IsNotFound(err))

	deleted, err := sc.Delete(ctx, "team-a", "missing", v1.DeleteOptions{AllowMissing: true})
	require.NoError(t, err)
	assert.Equal(t, types.DeletionAlreadyAbsent, deleted.Outcome)
}

func TestSecret_DeepCopy(t *testing.T) {
	sc := newTestSecretClient()
	ctx := context.Background()

	create := &types.SecretCreate{
		Name:       "oauth-client",
		Value:      "s3cr3t",
		RefreshUse: &types.SecretRefreshUse{AllowedEndpoints: []string{"https://idp.example.com/token"}},
		Labels:     map[string]string{"team": "platform"},
	}
	created, err := sc.Create(ctx, "team-a", create)
	require.NoError(t, err)

	create.Labels["team"] = "mutated"
	create.RefreshUse.AllowedEndpoints[0] = "mutated"
	created.Labels["team"] = "mutated"
	created.RefreshUse.AllowedEndpoints[0] = "mutated"

	got, err := sc.Get(ctx, "team-a", "oauth-client")
	require.NoError(t, err)
	assert.Equal(t, "platform", got.Labels["team"])
	assert.Equal(t, "https://idp.example.com/token", got.RefreshUse.AllowedEndpoints[0])
}

func TestSecret_ClosedReturnsUnavailable(t *testing.T) {
	store := newobjectStore(secretName, copySecret)
	sc := newFakeSecretClient(store, func() bool { return true })
	ctx := context.Background()

	_, err := sc.Create(ctx, "team-a", testSecretCreate("s"))
	assert.True(t, types.IsUnavailable(err))
	_, err = sc.Get(ctx, "team-a", "s")
	assert.True(t, types.IsUnavailable(err))
	_, err = sc.List("team-a")
	assert.True(t, types.IsUnavailable(err))
	_, err = sc.ListAll(ctx, "team-a")
	assert.True(t, types.IsUnavailable(err))
	_, err = sc.Update(ctx, "team-a", "s", &types.SecretUpdate{})
	assert.True(t, types.IsUnavailable(err))
	_, err = sc.Delete(ctx, "team-a", "s")
	assert.True(t, types.IsUnavailable(err))
}

func TestClient_SecretsAccessorAndSeeding(t *testing.T) {
	fc := NewClient()
	defer fc.Close() //nolint:errcheck
	ctx := context.Background()

	fc.AddSecret("team-a", nil)
	seed := &types.Secret{Name: "seeded", Labels: map[string]string{"team": "platform"}, ResourceVersion: 5}
	fc.AddSecret("team-a", seed)
	seed.Labels["team"] = "mutated"

	got, err := fc.Secrets().Get(ctx, "team-a", "seeded")
	require.NoError(t, err)
	assert.Equal(t, uint64(5), got.ResourceVersion)
	assert.Equal(t, "platform", got.Labels["team"])

	list, err := fc.Secrets().ListAll(ctx, "team-a")
	require.NoError(t, err)
	assert.Len(t, list, 1)
}
