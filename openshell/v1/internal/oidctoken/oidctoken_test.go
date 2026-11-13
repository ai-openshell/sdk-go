// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidctoken

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// TestBundle_ClientCredentialsMatchesCLI verifies that the
// client_credentials marker round-trips and, like the CLI, is omitted
// when false.
func TestBundle_ClientCredentialsMatchesCLI(t *testing.T) {
	var b Bundle
	require.NoError(t, json.Unmarshal([]byte(
		`{"access_token":"a","issuer":"https://issuer.example.com","client_id":"svc","client_credentials":true}`,
	), &b))
	assert.True(t, b.ClientCredentials)

	data, err := json.Marshal(b)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"client_credentials":true`)

	data, err = json.Marshal(FromToken(&oauth2.Token{AccessToken: "a", Expiry: time.Unix(1, 0)}, "https://issuer.example.com", "openshell-cli"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "client_credentials")
}
