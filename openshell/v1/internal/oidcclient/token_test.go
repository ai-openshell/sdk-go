// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// TestRetryReplace verifies the replace retry schedule: transient errors
// are retried with delays doubling from 5 ms up to 50 ms until they add
// up to 250 ms, and any other error is returned at once.
func TestRetryReplace(t *testing.T) {
	errTransient := errors.New("transient")
	errOther := errors.New("other")
	transient := func(err error) bool { return errors.Is(err, errTransient) }

	t.Run("succeeds after transient failures", func(t *testing.T) {
		var slept []time.Duration
		calls := 0
		err := retryReplace(func() error {
			calls++
			if calls < 3 {
				return errTransient
			}
			return nil
		}, transient, func(d time.Duration) { slept = append(slept, d) })
		require.NoError(t, err)
		assert.Equal(t, 3, calls)
		assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, slept)
	})

	t.Run("returns other errors at once", func(t *testing.T) {
		var slept []time.Duration
		err := retryReplace(func() error { return errOther }, transient,
			func(d time.Duration) { slept = append(slept, d) })
		require.ErrorIs(t, err, errOther)
		assert.Empty(t, slept)
	})

	t.Run("gives up after 250 ms of delays", func(t *testing.T) {
		var slept []time.Duration
		calls := 0
		err := retryReplace(func() error { calls++; return errTransient }, transient,
			func(d time.Duration) { slept = append(slept, d) })
		require.ErrorIs(t, err, errTransient)
		ms := time.Millisecond
		assert.Equal(t, []time.Duration{5 * ms, 10 * ms, 20 * ms, 40 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms}, slept)
		assert.Equal(t, len(slept)+1, calls)
	})
}

// TestIsTransientReplaceError verifies that only access denied and sharing
// violation on Windows are retried.
func TestIsTransientReplaceError(t *testing.T) {
	link := func(errno syscall.Errno) error {
		return &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errno}
	}
	assert.True(t, isTransientReplaceError("windows", link(5)))
	assert.True(t, isTransientReplaceError("windows", link(32)))
	assert.False(t, isTransientReplaceError("windows", link(2)))
	assert.False(t, isTransientReplaceError("windows", errors.New("rename failed")))
	assert.False(t, isTransientReplaceError("linux", link(5)))
	assert.False(t, isTransientReplaceError("darwin", link(32)))
}

// TestTokenFile_Errors verifies that failures to read or replace
// oidc_token.json are reported as ErrTokenPersist with the step that
// failed, and that a failed write leaves no temporary file behind.
func TestTokenFile_Errors(t *testing.T) {
	b := oidctoken.Bundle{AccessToken: "access", Issuer: "https://idp.example.com", ClientID: "client"}

	t.Run("token path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, oidctoken.FileName), 0o700))

		_, err := ReadToken(dir)
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.Contains(t, err.Error(), "cannot read")

		err = WriteToken(dir, b)
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.Contains(t, err.Error(), "failed to replace")
		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Len(t, entries, 1, "only the directory is left")
	})
	t.Run("missing directory", func(t *testing.T) {
		err := WriteToken(filepath.Join(t.TempDir(), "absent"), b)
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.Contains(t, err.Error(), "failed to create temporary token file")
	})
	t.Run("missing file", func(t *testing.T) {
		got, err := ReadToken(t.TempDir())
		require.NoError(t, err)
		assert.Nil(t, got)
	})
	t.Run("malformed expiry hides its value", func(t *testing.T) {
		dir := t.TempDir()
		const invalidExpiry = "123456789012345678901234567890"
		data := []byte(`{"access_token":"access","expires_at":` + invalidExpiry + `}`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, oidctoken.FileName), data, 0o600))

		_, err := ReadToken(dir)
		require.ErrorIs(t, err, ErrTokenPersist)
		assert.ErrorContains(t, err, "invalid JSON in "+oidctoken.FileName)
		assert.NotContains(t, err.Error(), invalidExpiry)
	})
}
