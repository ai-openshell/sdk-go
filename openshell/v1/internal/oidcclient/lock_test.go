// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	// Spellings a file system may treat as one directory: macOS and
	// Windows ignore case, and APFS also ignores Unicode normalization and
	// folds the Greek final sigma. Each pair runs only where the two names
	// are one directory.
	for name, pair := range map[string][2]string{
		"case":                {"gw", "GW"},
		"NFC and NFD":         {"caf\u00e9", "cafe\u0301"},
		"NFD and NFC":         {"cafe\u0301", "caf\u00e9"},
		"final sigma":         {"\u03c2", "\u03c3"},
		"capital sigma":       {"\u03c2", "\u03a3"},
		"sharp s and capital": {"\u00df", "\u1e9e"},
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			created := filepath.Join(parent, pair[0])
			require.NoError(t, os.Mkdir(created, 0o700))
			other := filepath.Join(parent, pair[1])
			if !sameDir(created, other) {
				t.Skipf("%q and %q are different directories here", pair[0], pair[1])
			}
			requireSharedLock(t, created, other)
		})
	}
	t.Run("firmlink", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("macOS firmlinks")
		}
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		alias := filepath.Join("/System/Volumes/Data", dir)
		if !sameDir(dir, alias) {
			t.Skip("no firmlink alias here")
		}
		requireSharedLock(t, dir, alias)
	})
	t.Run("different directories", func(t *testing.T) {
		parent := t.TempDir()
		a, b := filepath.Join(parent, "a"), filepath.Join(parent, "b")
		require.NoError(t, os.Mkdir(a, 0o700))
		require.NoError(t, os.Mkdir(b, 0o700))
		unlock, err := LockDir(context.Background(), a)
		require.NoError(t, err)
		defer unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		unlockB, err := LockDir(ctx, b)
		require.NoError(t, err, "b waited for the lock on a")
		unlockB()
	})
	t.Run("missing directory", func(t *testing.T) {
		parent := t.TempDir()
		missing := filepath.Join(parent, "missing")
		requireSharedLock(t, missing, filepath.Join(missing, "..", "missing"))
		t.Chdir(parent)
		requireSharedLock(t, "missing", missing)
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks needs privileges on Windows")
		}
		parent := t.TempDir()
		target := filepath.Join(parent, "real")
		require.NoError(t, os.Mkdir(target, 0o700))
		link := filepath.Join(parent, "link")
		require.NoError(t, os.Symlink(target, link))
		unlock, err := LockDir(context.Background(), target)
		require.NoError(t, err)
		defer unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = LockDir(ctx, link)
		require.ErrorIs(t, err, context.DeadlineExceeded, "the symlink did not wait for the lock on its target")
	})
}

// sameDir reports whether a and b name one existing directory.
func sameDir(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// requireSharedLock fails the test unless other waits for the lock taken
// on held.
func requireSharedLock(t *testing.T, held, other string) {
	t.Helper()
	unlock, err := LockDir(context.Background(), held)
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	unlockOther, err := LockDir(ctx, other)
	if err == nil {
		unlockOther()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded, "%q did not wait for the lock on %q", other, held)
}
