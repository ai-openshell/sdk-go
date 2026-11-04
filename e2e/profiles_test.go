// SPDX-FileCopyrightText: Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

// The gateway ships no provider profiles; every profile a provider uses must
// be imported first. These fixtures follow core's example profiles
// (providers/claude-code.yaml and providers/nvidia.yaml), reduced to the
// the credentials the tests rely on. They declare no endpoints: the gateway
// requires L7 rules, access, or allow_uninspected_credentials on a
// credentialed endpoint, and the SDK's NetworkEndpoint can't express those
// yet.
var testProfiles = []v1.ProviderProfile{
	{
		ID:               "claude-code",
		DisplayName:      "Claude Code",
		Description:      "E2E fixture based on core's providers/claude-code.yaml",
		Category:         v1.ProfileCategoryAgent,
		InferenceCapable: true,
		Credentials: []v1.ProfileCredential{{
			Name:       "api_key",
			EnvVars:    []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
			Required:   true,
			AuthStyle:  "header",
			HeaderName: "x-api-key",
		}},
	},
	{
		ID:               "nvidia",
		DisplayName:      "NVIDIA",
		Description:      "E2E fixture based on core's providers/nvidia.yaml",
		Category:         v1.ProfileCategoryInference,
		InferenceCapable: true,
		Credentials: []v1.ProfileCredential{{
			Name:     "api_key",
			EnvVars:  []string{"NVIDIA_API_KEY"},
			Required: true,
		}},
	},
}

// globalScope selects global provider profiles. A provider without a profile
// workspace resolves its type against them, as the tests' providers do.
const globalScope = ""

// importTestProfiles replaces the global fixtures, so a rerun against the
// same gateway starts from the same definitions.
func importTestProfiles(client *v1.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	profiles := client.Providers().Profiles()
	items := make([]v1.ProfileImportItem, 0, len(testProfiles))
	for _, p := range testProfiles {
		// A missing profile is the normal case; any other problem shows up in
		// the import below.
		_, _ = profiles.Delete(ctx, globalScope, p.ID)
		items = append(items, v1.ProfileImportItem{Profile: p, Source: "e2e/profiles_test.go"})
	}
	res, err := profiles.Import(ctx, globalScope, items)
	if err != nil {
		return fmt.Errorf("import provider profiles: %w", err)
	}
	if !res.Imported {
		return fmt.Errorf("import provider profiles: rejected: %+v", res.Diagnostics)
	}
	return nil
}

// boundCredentialPolicy is defaultPolicy plus one endpoint bound to the
// provider's credentials. The fixture profiles declare no endpoints, and the
// gateway withholds credentials of such profiles unless the sandbox policy
// binds them explicitly (core's
// test_endpointless_profile_credentials_use_explicit_policy_binding).
func boundCredentialPolicy(provider, host string) *v1.SandboxPolicy {
	p := defaultPolicy()
	p.NetworkPolicies = map[string]v1.NetworkPolicyRule{
		"e2e_credentials": {
			Name: "e2e_credentials",
			Endpoints: []v1.PolicyNetworkEndpoint{{
				Host:              host,
				Port:              443,
				Protocol:          "rest",
				Access:            v1.NetworkAccessPresetFull,
				CredentialBinding: &types.NetworkCredentialBinding{Provider: provider},
			}},
		},
	}
	return p
}
