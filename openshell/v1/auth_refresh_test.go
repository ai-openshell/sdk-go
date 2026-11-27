// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockTokenSource implements oauth2.TokenSource for testing.
type mockTokenSource struct {
	mu        sync.Mutex
	tokenFunc func() (*oauth2.Token, error)
	callCount int
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()
	return m.tokenFunc()
}

func (m *mockTokenSource) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// --- Phase 2 tests: constructor validation ---

func TestRefreshableToken_NilSource(t *testing.T) {
	_, err := RefreshableToken(nil)
	require.Error(t, err)
	assert.Equal(t, "openshell: TokenSource must not be nil", err.Error())
}

func TestRefreshableToken_ValidSource(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)
	assert.NotNil(t, provider)
}

func TestGetRequestMetadata_ReturnsWhenContextCanceledDuringRefresh(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	src := &mockTokenSource{tokenFunc: func() (*oauth2.Token, error) {
		<-release
		return &oauth2.Token{AccessToken: "late-token"}, nil
	}}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err = provider.GetRequestMetadata(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, codes.Canceled, status.Code(err), "gRPC reports the cancellation, not Unauthenticated")
	assert.Less(t, time.Since(started), time.Second)
}

// TestGetRequestMetadata_UsesCachedTokenDuringSlowRefresh verifies that
// while a refresh is slow and the cached token has not expired, a request
// with a deadline gets the cached token at half its deadline, a canceled
// request gets it too, and once the cached token has expired the request
// fails with the context's error.
func TestGetRequestMetadata_UsesCachedTokenDuringSlowRefresh(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	expiry := time.Now().Add(3 * time.Second)
	src := &mockTokenSource{tokenFunc: func() (*oauth2.Token, error) {
		if calls.Add(1) == 1 {
			// Inside the default 10 s leeway, so the next call refreshes.
			return &oauth2.Token{AccessToken: "cached-token", Expiry: expiry}, nil
		}
		<-release
		return &oauth2.Token{AccessToken: "late-token"}, nil
	}}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)
	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	md, err := provider.GetRequestMetadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bearer cached-token", md["authorization"])
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 800*time.Millisecond, "it waited about half the deadline")
	assert.Less(t, elapsed, 1500*time.Millisecond, "it gave up at half the deadline")

	canceled, cancelNow := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancelNow()
	}()
	md, err = provider.GetRequestMetadata(canceled)
	require.NoError(t, err)
	assert.Equal(t, "Bearer cached-token", md["authorization"])

	time.Sleep(time.Until(expiry) + 100*time.Millisecond)
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = provider.GetRequestMetadata(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	assert.Contains(t, err.Error(), "waiting for a token refresh")
}

// TestGetRequestMetadata_KeepsWaitingWhenCacheExpiresFirst verifies that a
// request that reaches half its deadline after the cached token expired
// keeps waiting for the refresh instead of sending the expired token.
func TestGetRequestMetadata_KeepsWaitingWhenCacheExpiresFirst(t *testing.T) {
	var calls atomic.Int32
	src := &mockTokenSource{tokenFunc: func() (*oauth2.Token, error) {
		if calls.Add(1) == 1 {
			return &oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(200 * time.Millisecond)}, nil
		}
		time.Sleep(time.Second)
		return &oauth2.Token{AccessToken: "late-token", Expiry: time.Now().Add(time.Hour)}, nil
	}}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)
	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	// Half the deadline is 600 ms, after the cached token expired at 200 ms.
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	md, err := provider.GetRequestMetadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bearer late-token", md["authorization"])
}

// TestGetRequestMetadata_RecordsRefreshNobodyWaitedFor verifies that the
// outcome of a refresh is kept even when every request stopped waiting for
// it: a failure starts the backoff, so the source is not asked on every
// request, and a new token is used without asking the source again.
func TestGetRequestMetadata_RecordsRefreshNobodyWaitedFor(t *testing.T) {
	newProvider := func(t *testing.T, refresh func() (*oauth2.Token, error)) (AuthProvider, *atomic.Int32) {
		t.Helper()
		var calls atomic.Int32
		src := &mockTokenSource{tokenFunc: func() (*oauth2.Token, error) {
			if calls.Add(1) == 1 {
				return &oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(5 * time.Second)}, nil
			}
			time.Sleep(150 * time.Millisecond)
			return refresh()
		}}
		provider, err := RefreshableToken(src)
		require.NoError(t, err)
		_, err = provider.GetRequestMetadata(context.Background())
		require.NoError(t, err)
		return provider, &calls
	}
	call := func(t *testing.T, provider AuthProvider) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		md, err := provider.GetRequestMetadata(ctx)
		require.NoError(t, err)
		return md["authorization"]
	}

	t.Run("failure starts the backoff", func(t *testing.T) {
		provider, calls := newProvider(t, func() (*oauth2.Token, error) { return nil, errors.New("provider down") })
		for range 10 {
			assert.Equal(t, "Bearer cached-token", call(t, provider))
			time.Sleep(100 * time.Millisecond)
		}
		// One initial fetch, the first refresh, and at most one more after
		// the 1 s backoff.
		assert.LessOrEqual(t, calls.Load(), int32(3))
	})
	t.Run("new token is used", func(t *testing.T) {
		provider, calls := newProvider(t, func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "new-token", Expiry: time.Now().Add(time.Hour)}, nil
		})
		assert.Equal(t, "Bearer cached-token", call(t, provider))
		time.Sleep(300 * time.Millisecond)
		assert.Equal(t, "Bearer new-token", call(t, provider))
		assert.Equal(t, int32(2), calls.Load())
	})
}

// --- Phase 3 / US1 tests: automatic token refresh ---

func TestGetRequestMetadata_FirstCallFetchesToken(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "fresh-token", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer fresh-token", md["authorization"])
	assert.Equal(t, 1, src.calls())
}

func TestGetRequestMetadata_CachedTokenNoExtraCall(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer cached-token", md["authorization"])
	assert.Equal(t, 1, src.calls(), "second call should use cache, not invoke TokenSource")
}

func TestGetRequestMetadata_RefreshesExpiredToken(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "old", Expiry: time.Now().Add(-time.Minute)}, nil
			}
			return &oauth2.Token{AccessToken: "new", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src, WithLeeway(0))
	require.NoError(t, err)

	// First call gets the expired token, which is immediately stale.
	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer old", md["authorization"])

	// Second call should trigger a refresh since the cached token is expired.
	md, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer new", md["authorization"])
	assert.Equal(t, 2, src.calls())
}

func TestGetRequestMetadata_ConcurrentSingleFlight(t *testing.T) {
	var fetchCount atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			fetchCount.Add(1)
			time.Sleep(100 * time.Millisecond)
			return &oauth2.Token{AccessToken: "shared-token", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ready := make(chan struct{})
	results := make([]string, goroutines)
	errs := make([]error, goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			<-ready
			md, e := provider.GetRequestMetadata(context.Background())
			errs[idx] = e
			if md != nil {
				results[idx] = md["authorization"]
			}
		}(i)
	}
	close(ready)
	wg.Wait()

	for i := range goroutines {
		require.NoError(t, errs[i], "goroutine %d failed", i)
		assert.Equal(t, "Bearer shared-token", results[i], "goroutine %d got wrong token", i)
	}
	assert.Equal(t, int32(1), fetchCount.Load(), "expected exactly 1 TokenSource.Token() call, got %d", fetchCount.Load())
}

func TestRefreshableAuth_RequireTransportSecurity(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "t"}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)
	assert.True(t, provider.RequireTransportSecurity())
}

// --- Phase 4 / US2 tests: graceful degradation ---

func TestGetRequestMetadata_RefreshFailureReturnsStaleCachedToken(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(5 * time.Second)}, nil
			}
			return nil, fmt.Errorf("idp unavailable")
		},
	}
	provider, err := RefreshableToken(src) // default 10s leeway
	require.NoError(t, err)

	// First call succeeds with a token inside the leeway window.
	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	// Second call: refresh fails, should return the unexpired stale token.
	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer stale", md["authorization"])
}

func TestGetRequestMetadata_RefreshFailureWithExpiredCachedTokenReturnsError(t *testing.T) {
	errIDP := errors.New("idp unavailable")
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "expired", Expiry: time.Now().Add(-time.Minute)}, nil
			}
			return nil, errIDP
		},
	}
	logger := &captureLogger{}
	provider, err := RefreshableToken(src, WithLeeway(0), WithLogger(logger))
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	md, err := provider.GetRequestMetadata(context.Background())
	require.ErrorIs(t, err, errIDP)
	assert.Nil(t, md)
	assert.Empty(t, logger.errors)

	// During backoff, the same error is returned without calling the source.
	md, err = provider.GetRequestMetadata(context.Background())
	require.ErrorIs(t, err, errIDP)
	assert.Nil(t, md)
	assert.Equal(t, 2, src.calls())
}

func TestGetRequestMetadata_RefreshFailureLogsWarning(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(5 * time.Second)}, nil
			}
			return nil, fmt.Errorf("idp unavailable")
		},
	}

	logger := &captureLogger{}
	provider, err := RefreshableToken(src, WithLogger(logger))
	require.NoError(t, err)

	_, _ = provider.GetRequestMetadata(context.Background())
	_, _ = provider.GetRequestMetadata(context.Background())

	require.Len(t, logger.errors, 1)
	assert.Contains(t, logger.errors[0].msg, "token refresh failed")
}

func TestGetRequestMetadata_RefreshFailureNoCachedTokenReturnsError(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return nil, fmt.Errorf("idp unavailable")
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idp unavailable")
}

func TestGetRequestMetadata_RefreshFailureNoLoggerNoPanic(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(5 * time.Second)}, nil
			}
			return nil, fmt.Errorf("idp unavailable")
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, _ = provider.GetRequestMetadata(context.Background())

	assert.NotPanics(t, func() {
		_, _ = provider.GetRequestMetadata(context.Background())
	})
}

// --- Phase 5 / US3 tests: configurable leeway ---

func TestGetRequestMetadata_DefaultLeewayTriggersRefresh(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			return &oauth2.Token{
				AccessToken: fmt.Sprintf("token-%d", n),
				Expiry:      time.Now().Add(5 * time.Second), // within default 10s leeway
			}, nil
		},
	}
	provider, err := RefreshableToken(src) // default 10s leeway
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	// Token expires in 5s, which is within 10s leeway, so next call should refresh.
	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-2", md["authorization"])
	assert.Equal(t, 2, src.calls())
}

func TestGetRequestMetadata_CustomLeewayTriggersRefresh(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			return &oauth2.Token{
				AccessToken: fmt.Sprintf("token-%d", n),
				Expiry:      time.Now().Add(25 * time.Second), // within custom 30s leeway
			}, nil
		},
	}
	provider, err := RefreshableToken(src, WithLeeway(30*time.Second))
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	// Token expires in 25s, which is within 30s leeway, so next call should refresh.
	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-2", md["authorization"])
	assert.Equal(t, 2, src.calls())
}

func TestGetRequestMetadata_ZeroExpiryNeverRefreshes(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "forever-token"}, nil // zero Expiry
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)

	// Call multiple times; should never refresh since expiry is zero.
	for range 10 {
		_, err = provider.GetRequestMetadata(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, 1, src.calls(), "zero-expiry token should never be refreshed")
}

// --- Backoff tests ---

func TestGetRequestMetadata_BackoffSkipsRefreshDuringWindow(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			if n == 1 {
				return &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(5 * time.Second)}, nil
			}
			return nil, fmt.Errorf("idp down")
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	beforeCount := src.calls()
	require.Equal(t, 2, beforeCount)

	ra := provider.(*refreshableAuth)
	ra.mu.Lock()
	ra.nextRetry = time.Now().Add(time.Minute)
	ra.mu.Unlock()

	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer stale", md["authorization"])
	assert.Equal(t, beforeCount, src.calls(), "should not call Token() during backoff window")
}

func TestGetRequestMetadata_BackoffWithoutCachedTokenReturnsLastError(t *testing.T) {
	errIDP := errors.New("idp down")
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return nil, errIDP
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(t, err)

	_, err = provider.GetRequestMetadata(context.Background())
	require.ErrorIs(t, err, errIDP)

	_, err = provider.GetRequestMetadata(context.Background())
	require.ErrorIs(t, err, errIDP)
	assert.Equal(t, 1, src.calls(), "should not call Token() during backoff window")
}

func TestGetRequestMetadata_NilTokenStartsBackoff(t *testing.T) {
	t.Run("no cached token", func(t *testing.T) {
		src := &mockTokenSource{
			tokenFunc: func() (*oauth2.Token, error) { return nil, nil },
		}
		provider, err := RefreshableToken(src)
		require.NoError(t, err)

		_, err = provider.GetRequestMetadata(context.Background())
		require.ErrorIs(t, err, errNilToken)

		_, err = provider.GetRequestMetadata(context.Background())
		require.ErrorIs(t, err, errNilToken)
		assert.Equal(t, 1, src.calls(), "a nil token must start the backoff")
	})

	t.Run("cached token", func(t *testing.T) {
		var callNum atomic.Int32
		src := &mockTokenSource{
			tokenFunc: func() (*oauth2.Token, error) {
				if callNum.Add(1) == 1 {
					return &oauth2.Token{AccessToken: "stale", Expiry: time.Now().Add(5 * time.Second)}, nil
				}
				return nil, nil
			},
		}
		provider, err := RefreshableToken(src) // default 10s leeway
		require.NoError(t, err)

		_, err = provider.GetRequestMetadata(context.Background())
		require.NoError(t, err)
		for range 2 {
			md, err := provider.GetRequestMetadata(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "Bearer stale", md["authorization"])
		}
		assert.Equal(t, 2, src.calls(), "a nil token must start the backoff")
	})
}

func TestGetRequestMetadata_BackoffResetsOnSuccess(t *testing.T) {
	var callNum atomic.Int32
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			n := callNum.Add(1)
			switch n {
			case 1:
				return &oauth2.Token{AccessToken: "initial", Expiry: time.Now().Add(-time.Second)}, nil
			case 2:
				return nil, fmt.Errorf("fail once")
			default:
				return &oauth2.Token{AccessToken: "recovered", Expiry: time.Now().Add(time.Hour)}, nil
			}
		},
	}
	provider, err := RefreshableToken(src, WithLeeway(0))
	require.NoError(t, err)

	_, _ = provider.GetRequestMetadata(context.Background())
	_, _ = provider.GetRequestMetadata(context.Background())

	ra := provider.(*refreshableAuth)
	ra.mu.Lock()
	ra.nextRetry = time.Time{}
	ra.mu.Unlock()

	md, err := provider.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer recovered", md["authorization"])

	ra.mu.Lock()
	assert.Equal(t, time.Duration(0), ra.backoff, "backoff should reset after success")
	assert.True(t, ra.nextRetry.IsZero(), "nextRetry should be zero after success")
	ra.mu.Unlock()
}

func TestGetRequestMetadata_BackoffCapsAt30s(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return nil, fmt.Errorf("always fail")
		},
	}
	provider, err := RefreshableToken(src, WithLeeway(0))
	require.NoError(t, err)

	ra := provider.(*refreshableAuth)

	for range 10 {
		ra.mu.Lock()
		ra.nextRetry = time.Time{}
		ra.mu.Unlock()

		_, _ = provider.GetRequestMetadata(context.Background())
	}

	ra.mu.Lock()
	assert.Equal(t, maxBackoff, ra.backoff, "backoff should cap at maxBackoff")
	ra.mu.Unlock()
}

func TestGetRequestMetadata_ConcurrentFailureBackoffNotOverIncremented(t *testing.T) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			time.Sleep(10 * time.Millisecond)
			return nil, fmt.Errorf("idp down")
		},
	}
	provider, err := RefreshableToken(src, WithLeeway(0))
	require.NoError(t, err)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ready := make(chan struct{})
	for range goroutines {
		go func() {
			defer wg.Done()
			<-ready
			_, _ = provider.GetRequestMetadata(context.Background())
		}()
	}
	close(ready)
	wg.Wait()

	ra := provider.(*refreshableAuth)
	ra.mu.Lock()
	assert.Equal(t, initialBackoff, ra.backoff,
		"a single coalesced failure should set backoff to initialBackoff, not escalate")
	ra.mu.Unlock()
	assert.Equal(t, 1, src.calls(), "singleflight should coalesce to 1 call")
}

// --- benchmarks ---

func BenchmarkGetRequestMetadata_CachedToken(b *testing.B) {
	src := &mockTokenSource{
		tokenFunc: func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "bench-token", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}
	provider, err := RefreshableToken(src)
	require.NoError(b, err)

	// Prime the cache.
	_, err = provider.GetRequestMetadata(context.Background())
	require.NoError(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = provider.GetRequestMetadata(context.Background())
	}
}

// --- helpers ---

type logEntry struct {
	err error
	msg string
}

type captureLogger struct {
	mu     sync.Mutex
	debugs []string
	infos  []string
	errors []logEntry
}

func (l *captureLogger) Debug(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugs = append(l.debugs, msg)
}

func (l *captureLogger) Info(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *captureLogger) Error(err error, msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, logEntry{err: err, msg: msg})
}
