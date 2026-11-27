// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

type readResult struct {
	code string
	err  error
}

// keyboardFlow implements the keyboard fallback for the authorization
// code flow. It displays the authorization URL to the user and reads
// the pasted authorization code from the provided reader.
//
// Parameters:
//   - ctx: context for cancellation/timeout
//   - authURL: the full authorization URL to display
//   - input: reader for user input (typically os.Stdin)
//   - output: writer for prompts/instructions (typically os.Stderr)
//
// Returns the authorization code or an error.
func keyboardFlow(ctx context.Context, authURL string, input io.Reader, output io.Writer) (string, error) {
	// Display instructions and URL.
	_, _ = fmt.Fprintf(output, "\nOpen the following URL in your browser to authenticate:\n\n  %s\n\n", authURL)
	_, _ = fmt.Fprint(output, "Paste the authorization code here and press Enter: ")

	// Read code with context cancellation support.
	result, err := keyboardInput.read(ctx, input)
	if err != nil {
		return "", err
	}
	if result.err != nil {
		return "", result.err
	}
	if result.code == "" {
		return "", fmt.Errorf("%w: empty authorization code", ErrAuthCode)
	}
	return result.code, nil
}

// inputDispatcher keeps one scanner per reader. A timed-out login stops
// waiting, and the line that scanner later reads stays queued for the next
// login instead of being delivered to the abandoned request.
type inputDispatcher struct {
	mu      sync.Mutex
	sources map[io.Reader]*lineSource
}

type lineSource struct {
	lines chan readResult
}

var keyboardInput = newInputDispatcher()

func newInputDispatcher() *inputDispatcher {
	return &inputDispatcher{sources: make(map[io.Reader]*lineSource)}
}

func (d *inputDispatcher) read(ctx context.Context, input io.Reader) (readResult, error) {
	if err := ctx.Err(); err != nil {
		return readResult{}, fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	src := d.source(input)
	select {
	case result, ok := <-src.lines:
		if !ok {
			return readResult{err: fmt.Errorf("%w: no authorization code received (EOF)", ErrAuthCode)}, nil
		}
		return result, nil
	case <-ctx.Done():
		return readResult{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
	}
}

func (d *inputDispatcher) source(input io.Reader) *lineSource {
	d.mu.Lock()
	defer d.mu.Unlock()
	if src, ok := d.sources[input]; ok {
		return src
	}
	src := &lineSource{lines: make(chan readResult, 1)}
	d.sources[input] = src
	go src.run(input)
	return src
}

func (s *lineSource) run(input io.Reader) {
	defer close(s.lines)
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		s.lines <- readResult{code: strings.TrimSpace(scanner.Text())}
	}
	if err := scanner.Err(); err != nil {
		s.lines <- readResult{err: fmt.Errorf("%w: failed to read authorization code: %v", ErrAuthCode, err)}
		return
	}
	s.lines <- readResult{err: fmt.Errorf("%w: no authorization code received (EOF)", ErrAuthCode)}
}
