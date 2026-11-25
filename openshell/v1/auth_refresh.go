// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/status"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/options"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

const (
	defaultLeeway  = 10 * time.Second
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

var errNilTokenSource = errors.New("openshell: TokenSource must not be nil")

// RefreshOption configures the behavior of RefreshableToken.
type RefreshOption func(*refreshConfig)

type refreshConfig struct {
	leeway time.Duration
	logger types.Logger
}

func defaultRefreshConfig() refreshConfig {
	return refreshConfig{
		leeway: defaultLeeway,
	}
}

// WithLeeway sets the duration before token expiry at which a proactive
// refresh is triggered. Default is 10 seconds.
func WithLeeway(d time.Duration) RefreshOption {
	return func(c *refreshConfig) {
		if d < 0 {
			d = 0
		}
		c.leeway = d
	}
}

// WithLogger sets the logger used for stale-token fallback warnings.
// When not set, warnings are silently dropped.
func WithLogger(l types.Logger) RefreshOption {
	return func(c *refreshConfig) {
		c.logger = l
	}
}

type refreshableAuth struct {
	source    oauth2.TokenSource
	mu        sync.Mutex
	group     singleflight.Group
	tok       *oauth2.Token
	leeway    time.Duration
	logger    types.Logger
	nextRetry time.Time
	backoff   time.Duration
	lastErr   error
}

// refreshWaitError is returned when a call's context ends while it waits for
// a refresh and no cached token is usable. It wraps the context's error
// and carries the matching gRPC status, so the call fails with
// DeadlineExceeded or Canceled rather than Unauthenticated.
type refreshWaitError struct{ err error }

func (e refreshWaitError) Error() string {
	return "openshell: waiting for a token refresh: " + e.err.Error()
}

func (e refreshWaitError) Unwrap() error { return e.err }

func (e refreshWaitError) GRPCStatus() *status.Status {
	return status.New(status.FromContextError(e.err).Code(), e.Error())
}

// cached returns the authorization metadata for the cached token if it
// has not expired.
func (r *refreshableAuth) cached() (map[string]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.usableCached() {
		return nil, false
	}
	return map[string]string{"authorization": "Bearer " + r.tok.AccessToken}, true
}

// usableCached reports whether the cached token has not yet expired.
// Callers must hold r.mu.
func (r *refreshableAuth) usableCached() bool {
	return r.tok != nil && (r.tok.Expiry.IsZero() || time.Now().Before(r.tok.Expiry))
}

func (r *refreshableAuth) isTokenValid() bool {
	if r.tok == nil {
		return false
	}
	if r.tok.Expiry.IsZero() {
		return true
	}
	leeway := r.leeway
	if source, ok := r.source.(interface {
		RefreshWindow() oidctoken.RefreshWindow
	}); ok {
		leeway = time.Duration(source.RefreshWindow())
	}
	return time.Now().Before(r.tok.Expiry.Add(-leeway))
}

func (r *refreshableAuth) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	r.mu.Lock()
	if r.isTokenValid() {
		tok := r.tok.AccessToken
		r.mu.Unlock()
		return map[string]string{"authorization": "Bearer " + tok}, nil
	}

	if !r.nextRetry.IsZero() && time.Now().Before(r.nextRetry) {
		if r.usableCached() {
			tok := r.tok.AccessToken
			r.mu.Unlock()
			return map[string]string{"authorization": "Bearer " + tok}, nil
		}
		err := r.lastErr
		r.mu.Unlock()
		return nil, err
	}
	usable := r.usableCached()
	r.mu.Unlock()

	resultCh := r.group.DoChan("refresh", func() (any, error) {
		tok, err := r.source.Token()
		if err == nil && tok == nil {
			err = errNilToken
		}
		// Record the outcome here rather than in the callers: with a
		// deadline, every caller may have stopped waiting for it.
		r.record(tok, err)
		return tok, err
	})

	// A cached token that has not expired is still good for this call, so
	// a call with a deadline waits for the refresh for at most half the
	// time it has left and keeps the rest for the call itself. The refresh
	// keeps running, and its outcome is recorded for later calls.
	var giveUp <-chan time.Time
	if deadline, ok := ctx.Deadline(); ok && usable {
		timer := time.NewTimer(time.Until(deadline) / 2)
		defer timer.Stop()
		giveUp = timer.C
	}
	var val any
	var err error
wait:
	for {
		select {
		case <-ctx.Done():
			if md, ok := r.cached(); ok {
				return md, nil
			}
			return nil, refreshWaitError{ctx.Err()}
		case <-giveUp:
			if md, ok := r.cached(); ok {
				return md, nil
			}
			giveUp = nil
		case result := <-resultCh:
			val = result.Val
			err = result.Err
			break wait
		}
	}

	if err != nil {
		if md, ok := r.cached(); ok {
			return md, nil
		}
		return nil, err
	}
	return map[string]string{"authorization": "Bearer " + val.(*oauth2.Token).AccessToken}, nil
}

// errNilToken is recorded when the source returns neither a token nor an
// error.
var errNilToken = errors.New("openshell: token source returned nil token without error")

// record stores a token or advances backoff on failure. A source that
// returns a still-valid token with no error resets this backoff and owns
// any cooldown for retries it makes. A failure with a usable cached token
// is logged once.
func (r *refreshableAuth) record(tok *oauth2.Token, err error) {
	r.mu.Lock()
	if err == nil {
		r.tok = tok
		r.backoff = 0
		r.nextRetry = time.Time{}
		r.lastErr = nil
		r.mu.Unlock()
		return
	}
	if r.nextRetry.IsZero() || !time.Now().Before(r.nextRetry) {
		bo := r.backoff
		if bo == 0 {
			bo = initialBackoff
		} else {
			bo *= 2
			if bo > maxBackoff {
				bo = maxBackoff
			}
		}
		r.backoff = bo
		r.nextRetry = time.Now().Add(bo)
	}
	r.lastErr = err
	logIt := r.logger != nil && r.usableCached()
	r.mu.Unlock()
	if logIt {
		r.logger.Error(err, "token refresh failed, using cached token")
	}
}

func (r *refreshableAuth) RequireTransportSecurity() bool {
	return true
}

// RefreshableToken returns an AuthProvider that caches tokens from src
// and refreshes them before expiry. Concurrent callers share a single
// in-flight refresh via singleflight. Failed refreshes trigger exponential
// backoff (1s, 2s, 4s, ..., 30s cap) to avoid amplifying token endpoint outages.
// After a failed refresh, the cached token is used only until it expires;
// after that, requests fail with the last refresh error. A source that
// returns neither a token nor an error is treated as a failed refresh.
// While a refresh runs and the cached token has not expired, a request with a deadline
// waits for the refresh for at most half the time it has left, then uses
// the cached token. A request whose context ends while it waits uses the
// cached token if it has not expired, and otherwise fails with an error
// that wraps the context's error and that gRPC reports as DeadlineExceeded
// or Canceled.
func RefreshableToken(src oauth2.TokenSource, opts ...RefreshOption) (AuthProvider, error) {
	if src == nil {
		return nil, errNilTokenSource
	}

	cfg := defaultRefreshConfig()
	options.Apply(&cfg, opts)

	return &refreshableAuth{
		source: src,
		leeway: cfg.leeway,
		logger: cfg.logger,
	}, nil
}
