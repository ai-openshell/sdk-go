// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package types

import "time"

// Secret is the metadata of a workspace secret. Secrets are credential minting
// material only: OpenShell never injects them into sandboxes. The value is
// write-only, so a Secret never carries it.
type Secret struct {
	ID                string
	Name              string
	CreatedAt         time.Time
	Labels            map[string]string
	Annotations       map[string]string
	ResourceVersion   uint64
	Workspace         string
	DeletionTimestamp *time.Time
	Description       string
	// RefreshUse is set only when the value may be used as provider refresh
	// material. It is fixed at creation.
	RefreshUse *SecretRefreshUse
	// ValueUpdatedAt is the last time the value was written.
	ValueUpdatedAt time.Time
}

// SecretRefreshUse allows provider refresh to use a secret's value.
type SecretRefreshUse struct {
	// AllowedEndpoints lists the token endpoints the value may be sent to
	// during refresh: absolute https URLs, or http URLs for loopback hosts.
	// The gateway requires at least one.
	AllowedEndpoints []string
}

// SecretCreate describes a workspace secret to create.
type SecretCreate struct {
	Name        string
	Description string
	// Value is the secret value. The gateway requires it to be non-empty.
	Value string
	// RefreshUse allows provider refresh to use the value. Nil creates a
	// reveal-only secret. It cannot be changed after creation.
	RefreshUse *SecretRefreshUse
	Labels     map[string]string
}

// SecretLabels is a complete label set for a workspace secret.
type SecretLabels struct {
	Labels map[string]string
}

// SecretUpdate describes changes to a workspace secret. A nil field leaves
// that part of the secret unchanged.
type SecretUpdate struct {
	// Value replaces the secret value. Nil leaves it unchanged; the gateway
	// rejects a present, empty value.
	Value *string
	// Description replaces the description. Nil leaves it unchanged; a pointer
	// to the empty string clears it.
	Description *string
	// Labels replaces the full label set. Nil leaves the labels unchanged; a
	// non-nil SecretLabels with a nil or empty map removes every label.
	Labels *SecretLabels
	// ExpectedResourceVersion is for optimistic concurrency (0 = skip check).
	ExpectedResourceVersion uint64
}
