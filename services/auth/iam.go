// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

// A Hanzo IAM access token IS a git credential here.
//
// Sign-in on this deployment is external — ENABLE_PASSWORD_SIGNIN_FORM is off and
// registration is external-only — so a user HAS no password to hand git over
// https, and the only credential left was a personal access token they had to go
// and make. That is a second credential, with a second lifetime and a second
// revocation, for an identity IAM already issues tokens for. This reads the one
// they already hold.
//
// WHAT IS CHECKED. The same reader every Hanzo service uses (hanzoai/authz over
// the issuer's JWKS): algorithm, key id, signature, issuer, expiry. Issuer and
// keys come from THIS instance's own OIDC login source, so the tokens accepted are
// the ones minted by the identity provider users already sign in through, and
// changing that provider changes this with it.
//
// WHOSE TOKEN IT IS is answered by the link the sign-in already wrote: the token's
// `sub` against external_login_user for that source. No claim is trusted to NAME a
// user — not the email, not the username — because those are mutable and a match
// on one would let a renamed or re-registered identity land on somebody else's
// account. A token whose subject was never linked here resolves to nobody.
//
// WHAT IT MAY DO is write:repository — clone, fetch, push. It is deliberately not
// the whole API: a credential that exists so a checkout works has no business
// minting tokens, adding keys, or administering anything, and the sandbox that
// carries one runs code its owner submitted.
//
// The AUDIENCE is not checked, matching the platform reader's documented position:
// IAM sets `aud` to the client that ASKED for the token, not to the server that may
// accept it, so checking it here would mean enumerating every client in the estate
// and 401ing every user of the next one.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/authz/edge"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/optional"
	"github.com/hanzoai/git/services/auth/source/oauth2"
)

// IAMTokenMethodName names this credential in the request's LoginMethod, so an
// audit of who pushed what can tell it from a personal access token.
const IAMTokenMethodName = "iam_token"

// jwksTTL is how long verified key material is reused. IAM rotates signing certs
// rarely and a rotation is picked up within this window; it is the same order as
// every other reader of the same JWKS.
const jwksTTL = 15 * time.Minute

// quiet is how long a FAILED discovery is remembered. Without it an unreachable
// provider is re-dialled by every request that carries anything JWT-shaped, each
// waiting out the timeout while holding the lock the next one needs — one
// unreachable host turning into a queue. Short, because the provider coming back
// should not take a quarter of an hour to notice.
const quiet = 30 * time.Second

// reader holds the verifier built from a login source, and the source it was built
// from so a re-registered provider rebuilds it instead of verifying against the
// old one forever.
var reader struct {
	sync.Mutex
	verifier *edge.Verifier
	sourceID int64
	discover string
	built    time.Time
}

// iamUser resolves the user a Hanzo IAM access token names, or nil when the
// credential is not one — an empty string, a personal access token, anything this
// instance's identity provider did not sign, or a subject that was never linked
// here. Nil is "not applicable", never "authorized".
func iamUser(ctx context.Context, token string) *user_model.User {
	// A JWT and nothing else. Personal access tokens and action tokens are hex, so
	// this declines them without a lookup and without touching the network.
	if strings.Count(token, ".") != 2 || !strings.HasPrefix(token, "ey") {
		return nil
	}
	v, sourceID := verifier(ctx)
	if v == nil {
		return nil
	}
	claims, err := v.VerifyRaw(token)
	if err != nil {
		// Trace, not Error: a credential that is not ours reaching here is ordinary,
		// and the auth chain has other methods left to try.
		log.Trace("IAM Authorization: token not accepted: %v", err)
		return nil
	}
	if claims.Subject == "" {
		return nil
	}
	link, ok, err := user_model.GetExternalLogin(ctx, sourceID, claims.Subject)
	if err != nil {
		log.Error("GetExternalLogin: %v", err)
		return nil
	}
	if !ok {
		log.Trace("IAM Authorization: subject is not linked to an account here")
		return nil
	}
	u, err := user_model.GetUserByID(ctx, link.UserID)
	if err != nil {
		log.Error("GetUserByID: %v", err)
		return nil
	}
	return u
}

// verifier returns the reader for this instance's OIDC login source and that
// source's id, rebuilding it when the source changes or the cache lapses.
//
// The DISCOVERY document is the source of both the issuer and the key set, so the
// two can never be configured to disagree: whatever answers as the provider users
// sign in through is what signs the tokens accepted here.
func verifier(ctx context.Context) (*edge.Verifier, int64) {
	reader.Lock()
	defer reader.Unlock()
	src, cfg := oidcSource(ctx)
	if src == nil {
		return nil, 0
	}
	fresh := reader.discover == cfg.OpenIDConnectAutoDiscoveryURL && reader.sourceID == src.ID
	if fresh && reader.verifier != nil && time.Since(reader.built) < jwksTTL {
		return reader.verifier, reader.sourceID
	}
	if fresh && reader.verifier == nil && time.Since(reader.built) < quiet {
		return nil, 0 // the last attempt failed and the provider is still being left alone
	}
	reader.verifier, reader.sourceID, reader.discover = nil, src.ID, cfg.OpenIDConnectAutoDiscoveryURL
	reader.built = time.Now()
	issuer, jwks := discover(ctx, cfg.OpenIDConnectAutoDiscoveryURL)
	if issuer == "" || jwks == "" {
		return nil, 0
	}
	// No audience allowlist — see the file comment. The issuer list is what fails
	// closed, and it always has exactly this instance's provider in it.
	reader.verifier = edge.NewVerifier(jwks, []string{issuer}, nil, jwksTTL)
	return reader.verifier, reader.sourceID
}

// oidcSource is the active OpenID Connect login source, or nil when this instance
// has none. The FIRST such source is the one: a deployment signs its users in
// through one identity provider, and accepting tokens from a second would accept
// an identity the first never issued.
func oidcSource(ctx context.Context) (*auth_model.Source, *oauth2.Source) {
	sources, err := db.Find[auth_model.Source](ctx, auth_model.FindSourcesOptions{
		IsActive:  optional.Some(true),
		LoginType: auth_model.OAuth2,
	})
	if err != nil {
		log.Error("FindSources: %v", err)
		return nil, nil
	}
	for _, s := range sources {
		cfg, ok := s.Cfg.(*oauth2.Source)
		if ok && cfg.OpenIDConnectAutoDiscoveryURL != "" {
			return s, cfg
		}
	}
	return nil, nil
}

// discover reads the issuer and the key set address out of an OIDC discovery
// document. Both empty on any failure, which leaves the credential unresolved
// rather than verified against a guess.
func discover(ctx context.Context, url string) (issuer, jwks string) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return "", ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error("IAM discovery unreachable: %v", err)
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Error("IAM discovery: status %d", resp.StatusCode)
		return "", ""
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &doc) != nil {
		log.Error("IAM discovery: unreadable document")
		return "", ""
	}
	return strings.TrimRight(doc.Issuer, "/"), doc.JWKSURI
}
