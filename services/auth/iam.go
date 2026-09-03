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
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/auth/httpauth"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
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
	// An account that may not sign in may not sign in HERE either. Every other
	// source in this chain asks (db, ldap, signin), and skipping it would leave
	// one credential that outlives a suspension: the password form is closed to
	// them and their tokens can be revoked, but the IAM token they already hold
	// would keep answering. Deactivation and prohibition are the same answer,
	// and an organisation is not somebody who signs in at all.
	if !u.IsIndividual() || !u.IsActive || u.ProhibitLogin {
		log.Trace("IAM Authorization: account may not sign in")
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
	// The login SOURCE if this instance has one, otherwise the configured issuer.
	//
	// Verifying a bearer needs the issuer's public keys and nothing else — no
	// client credential, no registered source. Requiring a source row here made
	// every credential this instance can verify depend on the prerequisites of
	// BROWSER SIGN-IN, so with no source configured a git push, a CI clone and a
	// package pull were all unverifiable, though not one of them presents
	// anything but a bearer.
	discover, sourceID := "", int64(0)
	if iss := strings.TrimRight(strings.TrimSpace(setting.IAM.Issuer), "/"); iss != "" {
		discover = iss + "/.well-known/openid-configuration"
	}
	if discover == "" {
		return nil, 0
	}
	fresh := reader.discover == discover && reader.sourceID == sourceID
	if fresh && reader.verifier != nil && time.Since(reader.built) < jwksTTL {
		return reader.verifier, reader.sourceID
	}
	if fresh && reader.verifier == nil && time.Since(reader.built) < quiet {
		return nil, 0 // the last attempt failed and the provider is still being left alone
	}
	reader.verifier, reader.sourceID, reader.discover = nil, sourceID, discover
	reader.built = time.Now()
	issuer, jwks := discoverAt(ctx, discover)
	if issuer == "" || jwks == "" {
		return nil, 0
	}
	// No audience allowlist — see the file comment. The issuer list is what fails
	// closed, and it always has exactly this instance's provider in it.
	reader.verifier = edge.NewVerifier(jwks, []string{issuer}, nil, jwksTTL)
	return reader.verifier, reader.sourceID
}


// discover reads the issuer and the key set address out of an OIDC discovery
// document. Both empty on any failure, which leaves the credential unresolved
// rather than verified against a guess.
func discoverAt(ctx context.Context, url string) (issuer, jwks string) {
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

// IAM accepts a Hanzo IAM access token the way every other service in the estate
// takes one — as a Bearer.
//
// The credential was already accepted here, but only through HTTP Basic, because
// that is how git hands a password over https and the check was written where git
// arrives (basic.go). Every other Hanzo service reads a Bearer, so an API caller
// sent one and got 401 — indistinguishable from a bad token, while the SAME token
// as Basic answered 200. Nothing in the refusal named the scheme, so the shape of
// the credential looked like the problem when only its envelope was.
//
// Scope is unchanged: write:repository, decided in one place below. This method
// widens HOW the token may be presented, never WHAT it may do.
type IAM struct{}

var _ Method = &IAM{}

// Name returns the name of this authentication method.
func (*IAM) Name() string { return IAMTokenMethodName }

// Verify reads a Bearer (or ?token=) credential and accepts it when IAM signed it
// for a subject linked to an account here. Declines everything else so the rest of
// the chain still runs — a Gitea token reaching this method is not ours to answer.
func (*IAM) Verify(req *http.Request, _ http.ResponseWriter, store DataStore, _ SessionStore) (*user_model.User, error) {
	token, ok := parseToken(req)
	if !ok {
		return nil, nil //nolint:nilnil // the auth method is not applicable
	}
	u := iamUser(req.Context(), token)
	if u == nil {
		return nil, nil //nolint:nilnil // not an IAM token, or its subject is unlinked
	}
	log.Trace("IAM Authorization: Valid IAM token for user[%d]", u.ID)
	setIAMTokenScope(store)
	return u, nil
}

// setIAMTokenScope is the ONE place an IAM token's authority is stated, so the git
// path (basic.go) and the API path above cannot drift into granting different
// things for the same credential.
func setIAMTokenScope(store DataStore) {
	store.GetData()["LoginMethod"] = IAMTokenMethodName
	store.GetData()["IsApiToken"] = true
	store.GetData()["ApiTokenScope"] = auth_model.AccessTokenScopeWriteRepository
}

// parseToken pulls the presented bearer out of a request.
//
// It moved here from the deleted oauth2 method, which was its only other caller:
// with the forge no longer issuing tokens, reading one is something only the IAM
// verifier does. The query-string forms stay because a git client and a package
// client both use them where a header is awkward, and DISABLE_QUERY_AUTH_TOKEN
// still governs whether they are honoured.
func parseToken(req *http.Request) (string, bool) {
	_ = req.ParseForm()
	if !setting.DisableQueryAuthToken {
		if token := req.Form.Get("token"); token != "" {
			return token, true
		}
		if token := req.Form.Get("access_token"); token != "" {
			return token, true
		}
	} else if req.Form.Get("token") != "" || req.Form.Get("access_token") != "" {
		log.Warn("API token sent in query string but DISABLE_QUERY_AUTH_TOKEN=true")
	}
	if auHead := req.Header.Get("Authorization"); auHead != "" {
		parsed, ok := httpauth.ParseAuthorizationHeader(auHead)
		if ok && parsed.BearerToken != nil {
			return parsed.BearerToken.Token, true
		}
	}
	return "", false
}
