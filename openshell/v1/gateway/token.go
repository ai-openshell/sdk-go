// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidcclient"
	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
	"github.com/ai-openshell/sdk-go/openshell/v1/types"
)

const (
	// edgeTokenFile is the primary edge token filename.
	edgeTokenFile = "edge_token"

	// cfTokenFile is the legacy Cloudflare token filename, used as
	// fallback when edge_token does not exist.
	cfTokenFile = "cf_token"
)

// readEdgeToken reads the edge token from the given directory. It tries
// edge_token first, then cf_token as a legacy fallback. The file content
// is trimmed of surrounding whitespace.
func readEdgeToken(dir string) (string, error) {
	// Try primary edge_token file.
	primary := filepath.Join(dir, edgeTokenFile)
	data, err := os.ReadFile(primary)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("%w: edge_token file is empty", ErrTokenLoad)
		}
		return token, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("%w: cannot read %s: %v", ErrTokenLoad, edgeTokenFile, err)
	}

	// Fallback to legacy cf_token file (only when edge_token is absent).
	legacy := filepath.Join(dir, cfTokenFile)
	data, err = os.ReadFile(legacy)
	if err != nil {
		return "", fmt.Errorf("%w: neither edge_token nor cf_token found in gateway directory", ErrTokenLoad)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%w: cf_token file is empty", ErrTokenLoad)
	}

	return token, nil
}

// refreshWindow is the gateway's default renewal window before expiry,
// matching the CLI and Python SDK.
const refreshWindow = 30 * time.Second

// refreshRetryDelay is how long the source waits after a failed renewal
// of a token that has not expired before it starts another. Sources copy
// it when they are made; tests change it first.
var refreshRetryDelay = 5 * time.Second

// refreshWait is how long RPCs that hold a token that has not expired wait
// for a renewal, counted from its start, before they use that token and
// let the renewal finish in the background. When the provider answers
// within it, and RefreshableToken has not stopped waiting at half the
// RPC's deadline, the RPC returns only after the new token is written, so
// a program that exits right after the RPC does not cut the token request
// off. Sources copy it when they are made; tests shorten it first.
var refreshWait = 2 * time.Second

// refreshWaitTimeout bounds how long a refresh waits for the refresh lock
// and for discovery. The token requests themselves run to the end, bounded
// by the HTTP client's timeout. Sources copy it when they are made; tests
// shorten it first.
var refreshWaitTimeout = 30 * time.Second

// writeUnsaved writes a renewed token that an earlier write could not
// store. Tests replace it to make the write fail.
var writeUnsaved = oidcclient.SavePendingToken

// diskTokenSource implements oauth2.TokenSource by reading oidc_token.json
// from disk on every Token() call. This allows the source to pick up
// tokens refreshed by the CLI or another SDK without a process restart.
// When metadata.json names the issuer and client ID, it refreshes tokens
// that are due and rewrites the file in the CLI's format.
type diskTokenSource struct {
	dir  string
	name string

	// issuer and clientID come from metadata.json. A token is refreshed
	// only when both are set and the file was issued for them, as Login
	// requires.
	issuer   string
	clientID string

	// scopes are sent with the refresh request; nil sends none.
	scopes []string

	// logger, when set, receives every failed renewal, and renewed tokens
	// that could not be written.
	logger types.Logger

	// wait, waitTimeout and retryDelay are refreshWait,
	// refreshWaitTimeout and refreshRetryDelay.
	wait, waitTimeout, retryDelay time.Duration

	mu sync.Mutex
	// lifetime is how long the last token this source got from a token
	// request was valid for, or zero before the first one.
	lifetime time.Duration
	// renewal is the renewal in progress, if any, and retryAt the
	// earliest time to start one after a renewal failed while the token
	// was still usable.
	renewal *renewal
	retryAt time.Time
	// unsaved is a renewed token that could not be written; see Token.
	unsaved *unsavedToken
	// background counts running renewals, for tests.
	background sync.WaitGroup
}

// unsavedToken is a renewed token that could not be written, as the file
// would hold it, and the access token the file held instead. rotated
// reports that its refresh token differs from the file's, which a
// provider that rotates refresh tokens has then revoked.
type unsavedToken struct {
	bundle  oidctoken.Bundle
	over    string
	rotated bool
}

// renewal is one run of refresh that callers of Token share.
type renewal struct {
	done chan struct{}
	// waitUntil is when callers that hold a token that has not expired
	// stop waiting for this renewal.
	waitUntil time.Time
	tok       *oauth2.Token
	err       error
}

// newDiskTokenSource returns a diskTokenSource for the gateway directory
// dir. Without metadata it reads the file and never refreshes.
func newDiskTokenSource(dir, name string) *diskTokenSource {
	return &diskTokenSource{dir: dir, name: name, wait: refreshWait, waitTimeout: refreshWaitTimeout, retryDelay: refreshRetryDelay}
}

// newOIDCTokenSource returns a diskTokenSource for the OIDC gateway cfg,
// which refreshes with the issuer, client ID and scopes in its
// metadata.json and reports failures that do not fail an RPC to logger.
func newOIDCTokenSource(cfg *Config, logger types.Logger) *diskTokenSource {
	src := newDiskTokenSource(cfg.Dir, cfg.Name)
	src.issuer = cfg.OIDCIssuer
	src.clientID = cfg.OIDCClientID
	if scopes := strings.Fields(cfg.OIDCScopes); len(scopes) > 0 {
		src.scopes = oidcclient.ScopesWithOpenID(scopes)
	}
	src.logger = logger
	return src
}

// Token reads and parses oidc_token.json, returning an oauth2.Token. The
// file is read on every call to pick up tokens refreshed elsewhere.
//
// A token that expires within the refresh window is renewed when the file
// has a refresh token and was issued for the issuer and client ID in
// metadata.json. Concurrent calls share one renewal. While the token has
// not expired, Token waits for the renewal until refreshWait after it
// started, then returns the current token and leaves the renewal to finish
// in the background; after a failed renewal, the next one starts no sooner
// than refreshRetryDelay later. An expired token, including one that
// expires during that wait, is renewed before Token returns. If that fails
// and no usable token has appeared in the file meanwhile, Token returns
// ErrTokenLoad with the reason, such as the provider's error code or HTTP
// status.
//
// A renewed token that cannot be written stays in memory: a provider that
// rotates refresh tokens has already revoked the one in the file. While
// the file still holds the token it replaced, Token uses it in place of
// the file's, and the next renewal writes it before anything else. While
// that write fails, a renewal sends no token request if the refresh token
// was rotated, since the provider may end the session when it sees the
// revoked one; the client then works until the token expires. A change to
// the file, such as a login or a token written by another process,
// discards it; a removed file fails as before.
//
// A renewal is not tied to the client: closing the client does not stop
// it, and a renewal still running when the process exits is cut off.
func (d *diskTokenSource) Token() (*oauth2.Token, error) {
	bundle, err := d.read()
	if err != nil {
		return nil, err
	}
	bundle = d.withUnsaved(bundle)

	tok := bundle.Token()
	window := time.Duration(d.RefreshWindow())
	if bundle.RefreshToken == "" || oidcclient.Fresh(tok, window) || d.issuer == "" || d.clientID == "" {
		return d.unexpired(tok)
	}
	if !oidcclient.SameClient(bundle, d.issuer, d.clientID) {
		if _, err := d.unexpired(tok); err != nil {
			return nil, d.loadError("OIDC token for gateway %q was issued by another issuer or client than metadata.json names", d.name)
		}
		return tok, nil
	}

	if _, err := d.unexpired(tok); err != nil {
		r := d.renew(window, bundle.AccessToken, true)
		<-r.done
		return r.tok, r.err
	}

	// The token is still usable, so an RPC waits only briefly for the
	// provider.
	r := d.renew(window, bundle.AccessToken, false)
	if r == nil {
		return tok, nil
	}
	// The wait ends at r.waitUntil, or when the token expires if that is
	// sooner; a wait cut short by expiry goes on until the renewal ends.
	// That case is decided by which bound was used, not by reading the
	// clock again, which a timer may beat by a little.
	end, expires := r.waitUntil, false
	if !tok.Expiry.After(end) {
		end, expires = tok.Expiry, true
	}
	timer := time.NewTimer(time.Until(end))
	defer timer.Stop()
	select {
	case <-r.done:
		if r.err == nil {
			return r.tok, nil
		}
		if time.Now().Before(tok.Expiry) {
			return tok, nil
		}
		return r.tok, r.err
	case <-timer.C:
		if !expires && time.Now().Before(tok.Expiry) {
			return tok, nil
		}
	}
	<-r.done
	return r.tok, r.err
}

// renew returns the renewal in progress or starts one. seen is the access
// token the caller read from the file. Unless force is set, it starts none
// within refreshRetryDelay of a failed renewal and returns nil.
func (d *diskTokenSource) renew(window time.Duration, seen string, force bool) *renewal {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.renewal != nil {
		return d.renewal
	}
	if !force && time.Now().Before(d.retryAt) {
		return nil
	}
	r := &renewal{done: make(chan struct{}), waitUntil: time.Now().Add(d.wait)}
	d.renewal = r
	d.background.Add(1)
	go func() {
		defer d.background.Done()
		r.tok, r.err = d.refresh(window, seen)
		d.mu.Lock()
		d.renewal = nil
		d.retryAt = time.Time{}
		if r.err != nil {
			d.retryAt = time.Now().Add(d.retryDelay)
		}
		d.mu.Unlock()
		close(r.done)
		if r.err != nil {
			d.logError(r.err, "OIDC token renewal failed")
		}
	}()
	return r
}

// refresh renews the stored token; seen is the access token read before
// the renewal started. A renewed token that could not be written is
// returned all the same and kept as d.unsaved.
func (d *diskTokenSource) refresh(window time.Duration, seen string) (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.waitTimeout)
	defer cancel()
	if rotated, err := d.saveUnsaved(ctx); err != nil && rotated {
		// The file's refresh token is revoked; sending it could make the
		// provider end the session.
		return nil, fmt.Errorf("%w: cannot store the renewed OIDC token for gateway %q: %w", ErrTokenLoad, d.name, err)
	}
	res, err := oidcclient.Refresh(ctx, oidcclient.Request{
		Dir:         d.dir,
		Issuer:      d.issuer,
		ClientID:    d.clientID,
		Scopes:      d.scopes,
		Window:      window,
		FinishGrant: true,
	})
	if res.Granted {
		d.observe(res.Token)
	}
	switch {
	case err == nil:
		return res.Token, nil
	case res.Granted:
		// The provider may have revoked the refresh token in the file, so
		// this token may be the only one that works.
		d.keepUnsaved(res)
		d.logError(err, "cannot store the renewed OIDC token")
		return res.Token, nil
	}

	// Another process may have refreshed the file meanwhile, for example
	// while this call waited for the lock. Its token is used if it is not
	// the one this renewal started from and has not expired, even inside
	// the window: with tokens shorter than the window, a just-renewed token
	// is already inside it.
	if current, readErr := d.read(); readErr == nil && oidcclient.SameClient(current, d.issuer, d.clientID) {
		t := current.Token()
		if _, expiredErr := d.unexpired(t); current.AccessToken != seen && expiredErr == nil {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w: cannot refresh the OIDC token for gateway %q: %w; run `openshell gateway login %s`",
		ErrTokenLoad, d.name, err, d.name)
}

// keepUnsaved keeps the token that res says could not be written.
func (d *diskTokenSource) keepUnsaved(res oidcclient.Result) {
	if res.Unwritten == nil || res.Replaced == nil {
		return
	}
	d.mu.Lock()
	d.unsaved = &unsavedToken{
		bundle:  *res.Unwritten,
		over:    res.Replaced.AccessToken,
		rotated: res.Unwritten.RefreshToken != res.Replaced.RefreshToken,
	}
	d.mu.Unlock()
}

// withUnsaved returns the unsaved token in place of bundle while the file
// still holds the token it replaced, and otherwise drops it.
func (d *diskTokenSource) withUnsaved(bundle *oidctoken.Bundle) *oidctoken.Bundle {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unsaved == nil {
		return bundle
	}
	if bundle.AccessToken != d.unsaved.over {
		d.unsaved = nil
		return bundle
	}
	b := d.unsaved.bundle
	return &b
}

// saveUnsaved writes the unsaved token, if any, under the refresh lock,
// unless the file no longer holds the token it replaced. On failure it
// also reports whether that token's refresh token was rotated.
func (d *diskTokenSource) saveUnsaved(ctx context.Context) (rotated bool, err error) {
	d.mu.Lock()
	u := d.unsaved
	d.mu.Unlock()
	if u == nil {
		return false, nil
	}
	if err := writeUnsaved(ctx, d.dir, u.bundle, u.over); err != nil {
		return u.rotated, err
	}
	d.mu.Lock()
	if d.unsaved == u {
		d.unsaved = nil
	}
	d.mu.Unlock()
	return false, nil
}

// read reads oidc_token.json and checks that it has an access token, an
// issuer and a client ID.
func (d *diskTokenSource) read() (*oidctoken.Bundle, error) {
	data, err := os.ReadFile(filepath.Join(d.dir, oidctoken.FileName))
	if err != nil {
		return nil, d.loadError("cannot read %s: %v", oidctoken.FileName, err)
	}

	var bundle oidctoken.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, d.loadError("invalid JSON in %s", oidctoken.FileName)
	}

	if bundle.AccessToken == "" {
		return nil, d.loadError("missing access_token in %s", oidctoken.FileName)
	}
	// Files from earlier Go SDK builds lack these fields and expires_at;
	// accepting them would treat their tokens as never expiring.
	if bundle.Issuer == "" || bundle.ClientID == "" {
		return nil, d.loadError("OIDC token for gateway %q is missing issuer or client_id", d.name)
	}
	return &bundle, nil
}

// unexpired returns tok, or ErrTokenLoad if it has expired.
func (d *diskTokenSource) unexpired(tok *oauth2.Token) (*oauth2.Token, error) {
	if !tok.Expiry.IsZero() && !time.Now().Before(tok.Expiry) {
		return nil, d.loadError("OIDC token for gateway %q expired at %s",
			d.name, tok.Expiry.UTC().Format(time.RFC3339))
	}
	return tok, nil
}

// RefreshWindow is shared by the SDK cache and Token. It uses half the
// last observed token lifetime when that is shorter than refreshWindow.
func (d *diskTokenSource) RefreshWindow() oidctoken.RefreshWindow {
	d.mu.Lock()
	defer d.mu.Unlock()
	if half := d.lifetime / 2; half > 0 && half < refreshWindow {
		return oidctoken.RefreshWindow(half)
	}
	return oidctoken.RefreshWindow(refreshWindow)
}

// observe records how long a token from a token request is valid for.
func (d *diskTokenSource) observe(tok *oauth2.Token) {
	if tok.Expiry.IsZero() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lifetime = time.Until(tok.Expiry)
}

// logError reports err to the logger, if there is one.
func (d *diskTokenSource) logError(err error, msg string) {
	if d.logger != nil {
		d.logger.Error(err, msg, "gateway", d.name)
	}
}

// loadError wraps ErrTokenLoad with the CLI login command that repairs
// the token file.
func (d *diskTokenSource) loadError(format string, args ...any) error {
	return fmt.Errorf("%w: %s; run `openshell gateway login %s`",
		ErrTokenLoad, fmt.Sprintf(format, args...), d.name)
}

// lazyEdgeAuth implements types.AuthProvider for the cloudflare_jwt auth
// mode. Token loading is deferred to GetRequestMetadata so that
// NewClient succeeds even when the token file is missing on disk (FR-007).
// Each authentication attempt reads the file; token load errors surface
// on the RPC that needs the token, even after an earlier successful read.
type lazyEdgeAuth struct {
	dir string
}

// GetRequestMetadata reads the edge token from disk on every call and
// returns it as a Bearer authorization header. Reading each time picks up
// tokens rewritten by the CLI without a process restart.
func (a *lazyEdgeAuth) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	token, err := readEdgeToken(a.dir)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"authorization": "Bearer " + token,
	}, nil
}

// RequireTransportSecurity returns true because Bearer tokens must not
// be sent over plaintext connections.
func (a *lazyEdgeAuth) RequireTransportSecurity() bool {
	return true
}
