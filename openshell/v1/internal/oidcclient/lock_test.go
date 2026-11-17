// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLockDir verifies that a done context never takes the lock, even when
// it is free, and that one directory is one lock however its path is
// written.
func TestLockDir(t *testing.T) {
	t.Run("done context", func(t *testing.T) {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// With the lock free, a bare select would pick either case.
		for range 64 {
			unlock, err := LockDir(ctx, dir)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, unlock)
		}
		unlock, err := LockDir(context.Background(), dir)
		require.NoError(t, err, "a failed attempt left the lock taken")
		unlock()
	})
	t.Run("relative and absolute path", func(t *testing.T) {
		parent := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(parent, "gw"), 0o700))
		t.Chdir(parent)
		unlock, err := LockDir(context.Background(), "gw")
		require.NoError(t, err)
		defer unlock()

		for _, path := range []string{filepath.Join(parent, "gw"), filepath.Join("gw", "..", "gw")} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			_, err := LockDir(ctx, path)
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded, "%s did not wait for the lock on gw", path)
		}
	})
}
