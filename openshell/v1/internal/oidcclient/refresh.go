// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package oidcclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/ai-openshell/sdk-go/openshell/v1/internal/oidctoken"
)

// LoginRefreshWindow is how long before expiry Login treats a stored token as
// due for refresh: the expiry margin of oauth2.Token.Valid.
const LoginRefreshWindow = 10 * time.Second

// now is the clock used to decide whether a token is fresh. Tests replace
// it.
var now = time.Now

// refreshLocks serializes refreshes of one token file within the process,
// so concurrent callers do not spend the same refresh token twice. It
// holds a one-slot semaphore for each token directory ever refreshed,
// which is a handful of gateways in practice.
var refreshLocks struct {
	mu sync.Mutex
	// dirs identifies directories by os.SameFile, so every spelling of a
	// directory shares one lock: a relative path, a symlink, another case
	// or Unicode normalization on a file system that ignores them, a
	// macOS firmlink.
	dirs []dirLock
	// paths holds the locks of directories that could not be stat'ed, by
	// absolute path.
	paths map[string]chan struct{}
}

type dirLock struct {
	info os.FileInfo
	sem  chan struct{}
}

// dirSemaphore returns the refresh semaphore of dir.
func dirSemaphore(dir string) chan struct{} {
	// Stat outside the mutex: a slow file system then holds up only this
	// caller. SameFile(info, info) loads the file ID now on Windows, where
	// os.Stat leaves it for the first comparison, so a stored entry still
	// identifies its directory after the directory is gone.
	info, err := os.Stat(dir)
	if err == nil && !os.SameFile(info, info) {
		err = errors.New("no file identity")
	}
	refreshLocks.mu.Lock()
	defer refreshLocks.mu.Unlock()
	if err != nil {
		key := filepath.Clean(dir)
		if abs, err := filepath.Abs(key); err == nil {
			key = abs
		}
		if refreshLocks.paths == nil {
			refreshLocks.paths = make(map[string]chan struct{})
		}
		sem, ok := refreshLocks.paths[key]
		if !ok {
			sem = make(chan struct{}, 1)
			refreshLocks.paths[key] = sem
		}
		return sem
	}
	for _, l := range refreshLocks.dirs {
		if os.SameFile(l.info, info) {
			return l.sem
		}
	}
	sem := make(chan struct{}, 1)
	refreshLocks.dirs = append(refreshLocks.dirs, dirLock{info: info, sem: sem})
	return sem
}

// LockDir waits for the refresh lock of dir until ctx ends, and returns
// the function that releases it. When ctx ends first, it returns ctx's
// error. All spellings of one directory share its lock.
func LockDir(ctx context.Context, dir string) (func(), error) {
	sem := dirSemaphore(dir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Fresh reports whether tok has an access token that does not expire
// within window. A token without an expiry is fresh. With a 10 second
// window it is the same test as oauth2.Token.Valid.
func Fresh(tok *oauth2.Token, window time.Duration) bool {
	if tok == nil || tok.AccessToken == "" {
		return false
	}
	return tok.Expiry.IsZero() || !tok.Expiry.Round(0).Add(-window).Before(now())
}

// Request describes a refresh of the token stored in a directory.
type Request struct {
	// Dir is the directory that holds oidc_token.json.
	Dir string

	// Issuer and ClientID identify the provider and client the stored
	// token must belong to. A file written for another issuer or client is
	// not refreshed.
	Issuer   string
	ClientID string

	// Scopes are sent as scope when not empty.
	Scopes []string

	// Window is how close to expiry the stored token may be and still be
	// returned without a refresh.
	Window time.Duration

	// FinishGrant lets the token requests, and the write of the result,
	// run to the end when ctx ends after discovery; the retry after
	// invalid_grant is then still sent. A provider that rotates refresh
	// tokens revokes the stored one once it accepts a request, so a request
	// cut off before the response arrives loses the new tokens.
	// HTTPClient's timeout still bounds each request.
	FinishGrant bool
}

// Result is the token Refresh returns.
type Result struct {
	// Token is the token to use.
	Token *oauth2.Token

	// Granted reports that Token comes from a token request this call
	// made, not from the file.
	Granted bool

	// When the write after a granted token request fails, Unwritten is the
	// token file Refresh tried to write and Replaced the one it was to
	// replace, which the file still holds.
	Unwritten, Replaced *oidctoken.Bundle
}

// Refresh renews the token stored in req.Dir with its refresh token and
// rewrites the file.
//
// The file is shared with the CLI, and providers that rotate refresh
// tokens revoke the old one on use. Under a per-directory lock the file is
// read again first, so a token another process or goroutine has just
// refreshed is returned as is. When the refresh is rejected with
// invalid_grant, the file is read once more: a fresh token another process
// wrote for the same issuer and client is returned as is, and one that is
// due, with a different refresh token, is refreshed once with that token.
// Like the Python SDK, it takes no file lock across processes. If the
// response has no refresh token, the stored one is kept.
//
// Before the token request, Refresh checks that it can create a file in
// req.Dir, so a directory without write permission does not cost the
// refresh token. The write can still fail after the token request, for
// example on a full disk; the Result then carries the new token. Before
// writing, it reads the file again: if the file was removed,
// as `openshell gateway logout` does, or now holds another refresh token,
// as after a login in another process, or is for another client, the new
// token is not written, so the logout or the newer login stands. A file it
// cannot read at that point is written over.
//
// ctx bounds the lock wait, discovery and, unless req.FinishGrant is set,
// the token requests. The errors wrap ctx's error while waiting for the
// lock, ErrNotRefreshable when there is nothing to refresh or the file
// changed during the refresh, ErrDiscovery, ErrRefresh, and
// ErrTokenPersist when the new token cannot be written. When the write
// fails after a successful token request, the Result still holds the new
// token, which the file does not; the file keeps the old refresh token, as
// with the CLI.
func Refresh(ctx context.Context, req Request) (Result, error) {
	unlock, err := LockDir(ctx, req.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("waiting for another refresh of %s: %w", oidctoken.FileName, err)
	}
	defer unlock()

	current, err := ReadToken(req.Dir)
	switch {
	case err != nil:
		return Result{}, fmt.Errorf("%w: %v", ErrNotRefreshable, err)
	case current == nil:
		return Result{}, fmt.Errorf("%w: %s does not exist", ErrNotRefreshable, oidctoken.FileName)
	case !sameClient(current, req):
		return Result{}, fmt.Errorf("%w: it was issued for another issuer or client", ErrNotRefreshable)
	}
	if tok := current.Token(); Fresh(tok, req.Window) {
		return Result{Token: tok}, nil
	}
	if current.RefreshToken == "" {
		return Result{}, fmt.Errorf("%w: it has no refresh token", ErrNotRefreshable)
	}
	stored := *current

	if err := checkWritable(req.Dir); err != nil {
		return Result{}, err
	}

	provider, err := Discover(ctx, req.Issuer)
	if err != nil {
		return Result{}, err
	}

	grantCtx := ctx
	if req.FinishGrant {
		grantCtx = context.WithoutCancel(ctx)
	}
	tok, err := refreshToken(grantCtx, provider.TokenEndpoint, req.ClientID, stored.RefreshToken, req.Scopes)
	if errors.Is(err, ErrInvalidGrant) {
		peer, readErr := ReadToken(req.Dir)
		if readErr != nil || peer == nil || !sameClient(peer, req) {
			return Result{}, fmt.Errorf("%w: %w", ErrNotRefreshable, err)
		}
		if peerTok := peer.Token(); Fresh(peerTok, req.Window) {
			return Result{Token: peerTok}, nil
		}
		if peer.RefreshToken == "" || peer.RefreshToken == stored.RefreshToken {
			return Result{}, fmt.Errorf("%w: %w", ErrNotRefreshable, err)
		}
		stored = *peer
		tok, err = refreshToken(grantCtx, provider.TokenEndpoint, req.ClientID, stored.RefreshToken, req.Scopes)
	}
	if err != nil {
		return Result{}, err
	}

	// Providers that do not rotate refresh tokens omit one from the
	// response; keep the stored token so the next refresh still works.
	if tok.RefreshToken == "" {
		tok.RefreshToken = stored.RefreshToken
	}

	// Without a file lock across processes this narrows, but does not
	// close, the gap in which a logout or login can be overwritten. A file
	// that cannot be read now, for example while the CLI rewrites it in
	// place, is written over: the new token is the only one known to work.
	b := oidctoken.FromToken(tok, stored.Issuer, stored.ClientID)
	b.ClientCredentials = stored.ClientCredentials
	latest, attempted, err := replaceTokenIfCurrent(req.Dir, b, func(current *oidctoken.Bundle) bool {
		return sameClient(current, req) && current.RefreshToken == stored.RefreshToken
	}, true)
	if !attempted {
		if latest != nil && sameClient(latest, req) && Fresh(latest.Token(), req.Window) {
			return Result{Token: latest.Token()}, nil
		}
		return Result{}, fmt.Errorf("%w: %s changed during the refresh", ErrNotRefreshable, oidctoken.FileName)
	}

	granted := Result{Token: tok, Granted: true}
	if err != nil {
		granted.Unwritten, granted.Replaced = &b, &stored
		if latest != nil {
			granted.Replaced = latest
		}
		return granted, err
	}
	return granted, nil
}

// ScopesWithOpenID returns scopes with openid first and only once, as the
// authorization request sends them. It mirrors build_scopes in
// crates/openshell-cli/src/oidc_auth.rs.
func ScopesWithOpenID(scopes []string) []string {
	normalized := make([]string, 0, len(scopes)+1)
	normalized = append(normalized, scopeOpenID)
	for _, scope := range scopes {
		if scope != scopeOpenID {
			normalized = append(normalized, scope)
		}
	}
	return normalized
}

const scopeOpenID = "openid"

// SameClient reports whether a stored bundle was issued for issuer
// (ignoring a trailing slash) and clientID.
func SameClient(b *oidctoken.Bundle, issuer, clientID string) bool {
	return NormalizeIssuer(b.Issuer) == NormalizeIssuer(issuer) && b.ClientID == clientID
}

func sameClient(b *oidctoken.Bundle, req Request) bool {
	return SameClient(b, req.Issuer, req.ClientID)
}

// maxErrorCode is how much of a provider's error code goes into an error
// message. RFC 6749 error codes are short ASCII words.
const maxErrorCode = 64

// holdsSecret reports whether s contains secret as is or after undoing
// up to four rounds of percent-encoding, in either hex case.
func holdsSecret(s, secret string) bool {
	for range 4 {
		if strings.Contains(s, secret) {
			return true
		}
		decoded := percentDecode(s)
		if decoded == s {
			return false
		}
		s = decoded
	}
	return strings.Contains(s, secret)
}

// percentDecode decodes every valid %XX escape in s and leaves the rest,
// including malformed escapes, as is.
func percentDecode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, lo := unhex(s[i+1]), unhex(s[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

// grantResponse is the token endpoint's JSON response.
type grantResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

// refreshToken exchanges a refresh token for new tokens at the token
// endpoint (RFC 6749 Section 6). Like the CLI, it sends grant_type,
// refresh_token and client_id as a public client, plus scope when scopes
// are given.
//
// The refresh token is never included in error messages, even when the
// provider echoes it in its error code. The error code is quoted and cut
// to maxErrorCode bytes, and error_description is left out, since the
// provider controls that text. A response that is not JSON is reported by
// its HTTP status.
func refreshToken(ctx context.Context, tokenEndpoint, clientID, refresh string, scopes []string) (*oauth2.Token, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {clientID},
	}
	if len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create token request: %v", ErrRefresh, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token request failed: %v", ErrRefresh, err)
	}
	defer func() { _ = resp.Body.Close() }()

	const maxResponseBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read token response: %v", ErrRefresh, err)
	}

	// An error page from a proxy is not JSON; its status still says what
	// happened. So an error code is reported whatever the status, and a
	// body that is not JSON only for a 200 response.
	var tokResp grantResponse
	jsonErr := json.Unmarshal(body, &tokResp)
	if tokResp.Error == "invalid_grant" {
		return nil, fmt.Errorf("%w: %w", ErrRefresh, ErrInvalidGrant)
	}
	if code := tokResp.Error; code != "" {
		if refresh != "" {
			code = strings.ReplaceAll(code, refresh, "[refresh token]")
			code = strings.ReplaceAll(code, url.QueryEscape(refresh), "[refresh token]")
			if holdsSecret(code, refresh) {
				// Encoded some other way, such as with lowercase hex.
				code = "[error code containing the refresh token]"
			}
		}
		if len(code) > maxErrorCode {
			code = strings.ToValidUTF8(code[:maxErrorCode], "") + "..."
		}
		return nil, fmt.Errorf("%w: %q", ErrRefresh, code)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrRefresh, resp.StatusCode)
	}
	if jsonErr != nil {
		// Decoder errors can quote values from the response.
		return nil, fmt.Errorf("%w: invalid token response JSON", ErrRefresh)
	}
	if tokResp.AccessToken == "" {
		return nil, fmt.Errorf("%w: token response has no access_token", ErrRefresh)
	}

	// An absent or null expires_in leaves the token without an expiry, as
	// the code exchange and the CLI's oauth2 crate do. A present expires_in
	// must be positive; zero or a negative value is rejected as malformed.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	if raw, present := fields["expires_in"]; present && string(raw) != "null" && tokResp.ExpiresIn <= 0 {
		return nil, fmt.Errorf("%w: token response requires a positive expires_in", ErrRefresh)
	}

	tok := &oauth2.Token{
		AccessToken:  tokResp.AccessToken,
		RefreshToken: tokResp.RefreshToken,
		TokenType:    tokResp.TokenType,
	}
	if tokResp.ExpiresIn > 0 {
		// Capped at 2^31-1 seconds as golang.org/x/oauth2 does: above about
		// 9.2e9 seconds the multiplication overflows time.Duration and can
		// put the expiry in the past.
		tok.Expiry = time.Now().Add(time.Duration(min(tokResp.ExpiresIn, math.MaxInt32)) * time.Second)
	}
	return tok, nil
}
