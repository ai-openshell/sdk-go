// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// writeToken persists an OIDC token bundle to disk as oidc_token.json in
// the given directory, in the CLI's format. The file is written with 0600
// permissions (owner-only) to protect credentials, and on Windows the final
// replace is retried briefly while another process has the file open.
func writeToken(dir string, b oidctoken.Bundle) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("%w: failed to marshal token: %v", ErrTokenPersist, err)
	}

	path := filepath.Join(dir, oidctoken.FileName)
	tmp, err := os.CreateTemp(dir, ".oidc-token-*")
	if err != nil {
		return fmt.Errorf("%w: failed to create temporary token file: %v", ErrTokenPersist, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: failed to secure temporary token file: %v", ErrTokenPersist, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: failed to write %s: %v", ErrTokenPersist, path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: failed to sync %s: %v", ErrTokenPersist, path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: failed to close %s: %v", ErrTokenPersist, path, err)
	}
	err = retryReplace(
		func() error { return os.Rename(tmpPath, path) },
		func(err error) bool { return isTransientReplaceError(runtime.GOOS, err) },
		time.Sleep,
	)
	if err != nil {
		return fmt.Errorf("%w: failed to replace %s: %v", ErrTokenPersist, path, err)
	}

	return nil
}

// Windows replace retry timings, the same as _atomic_replace in the
// Python SDK.
const (
	replaceRetryTimeout      = 250 * time.Millisecond
	replaceRetryInitialDelay = 5 * time.Millisecond
	replaceRetryMaxDelay     = 50 * time.Millisecond
)

// retryReplace calls rename until it succeeds, fails with an error that
// transient rejects, or the delays between attempts add up to
// replaceRetryTimeout. The delay starts at replaceRetryInitialDelay and
// doubles up to replaceRetryMaxDelay.
func retryReplace(rename func() error, transient func(error) bool, sleep func(time.Duration)) error {
	delay := replaceRetryInitialDelay
	var waited time.Duration
	for {
		err := rename()
		if err == nil || !transient(err) || waited >= replaceRetryTimeout {
			return err
		}
		sleep(delay)
		waited += delay
		delay = min(delay*2, replaceRetryMaxDelay)
	}
}

// isTransientReplaceError reports whether a failed rename on goos may
// succeed if tried again. On Windows the replace fails with access denied
// or a sharing violation while another process, such as the CLI, another
// SDK or a scanner, has oidc_token.json open. A refresh may already have
// rotated the token by then, so giving up would lose it.
func isTransientReplaceError(goos string, err error) bool {
	const (
		errorAccessDenied     syscall.Errno = 5
		errorSharingViolation syscall.Errno = 32
	)
	var errno syscall.Errno
	return goos == "windows" && errors.As(err, &errno) &&
		(errno == errorAccessDenied || errno == errorSharingViolation)
}

// readToken reads an existing oidc_token.json from the given
// directory. It returns:
//   - (bundle, nil) if the file exists and parses
//   - (nil, nil) if the file does not exist
//   - (nil, error) if the file exists but cannot be read or parsed
func readToken(dir string) (*oidctoken.Bundle, error) {
	path := filepath.Join(dir, oidctoken.FileName)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: cannot read %s: %v", ErrTokenPersist, path, err)
	}

	var bundle oidctoken.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON in %s: %v", ErrTokenPersist, oidctoken.FileName, err)
	}

	return &bundle, nil
}
