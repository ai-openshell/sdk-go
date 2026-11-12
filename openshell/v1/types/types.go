// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package types

import "time"

// SandboxPhase represents the lifecycle phase of a sandbox.
type SandboxPhase string

// SandboxPhase values for sandbox lifecycle.
const (
	SandboxProvisioning SandboxPhase = "Provisioning"
	SandboxReady        SandboxPhase = "Ready"
	SandboxError        SandboxPhase = "Error"
	SandboxDeleting     SandboxPhase = "Deleting"
	SandboxUnknown      SandboxPhase = "Unknown"
	SandboxStopping     SandboxPhase = "Stopping"
	SandboxStopped      SandboxPhase = "Stopped"
	SandboxStarting     SandboxPhase = "Starting"
	SandboxCompleted    SandboxPhase = "Completed"
	SandboxSuspending   SandboxPhase = "Suspending"
	SandboxSuspended    SandboxPhase = "Suspended"
)

// SandboxRestartPolicy controls replacement after the canonical main process exits.
type SandboxRestartPolicy string

// Sandbox restart policy values.
const (
	SandboxRestartNever     SandboxRestartPolicy = "Never"
	SandboxRestartOnFailure SandboxRestartPolicy = "OnFailure"
	SandboxRestartAlways    SandboxRestartPolicy = "Always"
)

// EventType classifies watch events.
type EventType string

// EventType values for watch events.
const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
	EventError    EventType = "ERROR"
)

// StreamType identifies which output stream a chunk belongs to.
type StreamType string

// StreamType values for exec output.
const (
	StreamStdout StreamType = "stdout"
	StreamStderr StreamType = "stderr"
)

// TLSConfig holds TLS connection settings.
type TLSConfig struct {
	CertFile string
	KeyFile  string
	CAFile   string
	Insecure bool
}

// RetryPolicy configures retries of explicitly classified safe unary reads,
// only for original gRPC Unavailable status. Mutations, execution, session
// creation, streams, and unclassified methods are never replayed by this policy.
// Negative fields or effective MaxWait below InitialWait fail client construction
// with ErrorInvalidArgument. Each client snapshots the policy.
type RetryPolicy struct {
	// MaxRetries counts additional attempts after the initial call; zero disables retries.
	MaxRetries int
	// InitialWait defaults to 100 ms when zero. Backoff doubles before each later
	// retry, saturating at MaxWait without jitter or server retry hints.
	InitialWait time.Duration
	// MaxWait defaults to 5 s when zero. Cancellation and the shared unary deadline
	// interrupt backoff and prevent further attempts.
	MaxWait time.Duration
}
