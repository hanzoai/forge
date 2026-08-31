// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/git/modules/reqctx"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/services/auth/source/oauth2"
)

// A signed-in user's IAM token is a git credential, and the properties that make
// that safe are the ones tested here: only this instance's issuer is accepted,
// only a subject the sign-in already linked resolves, an expired token resolves
// to nobody, and a credential that is not a JWT is declined without a lookup.

// idp stands up an identity provider: a discovery document, a key set, and the
// key to sign tokens the verifier will accept.
type idp struct {
	url string
	key *rsa.PrivateKey
	kid string
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	p := &idp{key: key, kid: "test-key"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.url, "jwks_uri": p.url + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid, "n": n, "e": e},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

// sign mints an access token for sub, as issuer iss, living for life.
func (p *idp) sign(t *testing.T, iss, sub string, life time.Duration) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    iss,
		Subject:   sub,
		Audience:  jwt.ClaimStrings{"hanzo-cli"},
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(life)),
	})
	tok.Header["kid"] = p.kid
	s, err := tok.SignedString(p.key)
	require.NoError(t, err)
	return s
}

// register makes p this instance's OIDC login source and clears the verifier the
// previous case built, so each case reads its own provider.
func register(t *testing.T, p *idp) *auth_model.Source {
	t.Helper()
	src := &auth_model.Source{
		Type:     auth_model.OAuth2,
		Name:     "hanzo-" + t.Name(),
		IsActive: true,
		Cfg: &oauth2.Source{
			Provider:                      "openidConnect",
			ClientID:                      "hanzo-git",
			OpenIDConnectAutoDiscoveryURL: p.url + "/.well-known/openid-configuration",
		},
	}
	require.NoError(t, auth_model.CreateSource(t.Context(), src))
	reader.Lock()
	reader.verifier, reader.sourceID, reader.discover = nil, 0, ""
	reader.Unlock()
	return src
}

// link records what a sign-in through the source would have recorded: this IAM
// subject is this account.
func link(t *testing.T, sourceID int64, sub string, u *user_model.User) {
	t.Helper()
	require.NoError(t, user_model.LinkExternalToUser(t.Context(), u, &user_model.ExternalLoginUser{
		ExternalID:    sub,
		UserID:        u.ID,
		LoginSourceID: sourceID,
		Provider:      "openidConnect",
		Email:         u.Email,
	}))
}

func TestIAMTokenResolvesTheLinkedAccount(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	p := newIDP(t)
	src := register(t, p)
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	link(t, src.ID, "sub-of-user-2", u)

	got := iamUser(t.Context(), p.sign(t, p.url, "sub-of-user-2", time.Hour))
	require.NotNil(t, got, "a token for a linked subject resolves")
	assert.Equal(t, u.ID, got.ID)
}

func TestIAMTokenRefusedWhenTheSubjectIsNotLinked(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	p := newIDP(t)
	register(t, p)

	// A perfectly valid token for somebody who has never signed in here. There is
	// no account to act as, and nothing is created from a credential.
	assert.Nil(t, iamUser(t.Context(), p.sign(t, p.url, "a-stranger", time.Hour)))
}

func TestIAMTokenRefusedFromAnotherIssuer(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	p := newIDP(t)
	src := register(t, p)
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	link(t, src.ID, "sub-of-user-2", u)

	// Signed with the right key, for a linked subject, naming a DIFFERENT issuer.
	// The issuer is who signed it, so an unrecognised one is refused outright.
	assert.Nil(t, iamUser(t.Context(), p.sign(t, "https://elsewhere.example", "sub-of-user-2", time.Hour)))
}

func TestIAMTokenRefusedWhenExpired(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	p := newIDP(t)
	src := register(t, p)
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	link(t, src.ID, "sub-of-user-2", u)

	// Beyond the verifier's clock-skew allowance, so this is expiry and not skew.
	assert.Nil(t, iamUser(t.Context(), p.sign(t, p.url, "sub-of-user-2", -10*time.Minute)))
}

func TestIAMDeclinesCredentialsThatAreNotTokens(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	// No login source registered at all: nothing may resolve, and a personal access
	// token's shape is declined before any provider is consulted.
	reader.Lock()
	reader.verifier, reader.sourceID, reader.discover = nil, 0, ""
	reader.Unlock()
	for _, cred := range []string{"", "0123456789abcdef0123456789abcdef01234567", "not.a.jwt.at.all", "ey-almost"} {
		assert.Nil(t, iamUser(t.Context(), cred), "credential %q", cred)
	}
}

// AN UNREACHABLE PROVIDER IS DIALLED ONCE, not once per request. Without that, a
// credential that is merely JWT-shaped is enough to make every request wait out a
// network timeout while holding the lock the next one needs.
func TestAnUnreachableProviderIsLeftAlone(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	var down bool
	var dials int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if down {
			dials++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"})
	})

	// Registered while the provider answers — CreateSource discovers it too.
	src := &auth_model.Source{
		Type: auth_model.OAuth2, Name: "down-" + t.Name(), IsActive: true,
		Cfg: &oauth2.Source{Provider: "openidConnect", ClientID: "hanzo-git",
			OpenIDConnectAutoDiscoveryURL: srv.URL + "/.well-known/openid-configuration"},
	}
	require.NoError(t, auth_model.CreateSource(t.Context(), src))
	down = true
	reader.Lock()
	reader.verifier, reader.sourceID, reader.discover = nil, 0, ""
	reader.Unlock()

	for range 5 {
		assert.Nil(t, iamUser(t.Context(), "eyJhbGciOiJSUzI1NiJ9.e30.sig"))
	}
	assert.Equal(t, 1, dials, "the provider was dialled once per request")
}

// TestIAMTokenRefusedWhenTheAccountMayNotSignIn pins the gate every other source
// in this chain applies (db, ldap, signin). Without it this is the one credential
// that outlives a suspension: the password form is already closed to these
// accounts and their tokens can be revoked, but the IAM token they hold would go
// on answering. The subtests are the three separate ways an account is not
// somebody who signs in, so a fix that catches one and misses the others fails
// here rather than in production.
func TestIAMTokenRefusedWhenTheAccountMayNotSignIn(t *testing.T) {
	for name, set := range map[string]func(t *testing.T, u *user_model.User){
		"prohibited": func(t *testing.T, u *user_model.User) {
			_, err := db.GetEngine(t.Context()).ID(u.ID).Cols("prohibit_login").Update(&user_model.User{ProhibitLogin: true})
			require.NoError(t, err)
		},
		"deactivated": func(t *testing.T, u *user_model.User) {
			_, err := db.GetEngine(t.Context()).ID(u.ID).Cols("is_active").Update(&user_model.User{IsActive: false})
			require.NoError(t, err)
		},
		"not an individual": func(t *testing.T, u *user_model.User) {
			_, err := db.GetEngine(t.Context()).ID(u.ID).Cols("type").Update(&user_model.User{Type: user_model.UserTypeOrganization})
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			p := newIDP(t)
			src := register(t, p)
			u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
			link(t, src.ID, "sub-of-user-2", u)

			// The same token that resolves in TestIAMTokenResolvesTheLinkedAccount.
			token := p.sign(t, p.url, "sub-of-user-2", time.Hour)
			require.NotNil(t, iamUser(t.Context(), token), "the token resolves before the account is changed")

			set(t, u)
			assert.Nil(t, iamUser(t.Context(), token), "an account that may not sign in must not authenticate")
		})
	}
}

// mockDataStore is the smallest ContextDataProvider a Method needs: the map it
// writes LoginMethod and the token scope into.
type mockDataStore struct{ data reqctx.ContextData }

func (m *mockDataStore) GetData() reqctx.ContextData {
	if m.data == nil {
		m.data = reqctx.ContextData{}
	}
	return m.data
}

// TestIAMTokenIsAcceptedAsABearer pins the scheme, which is the half that was
// missing.
//
// The credential was only ever offered to the verifier through HTTP Basic, because
// that is how git hands a password over https and the check lived where git
// arrives. Every other Hanzo service reads a Bearer, so an API caller sent one and
// got 401 — the same answer a forged token gets — while the SAME token presented
// as Basic answered 200. The refusal never named the scheme, so the credential
// looked wrong when only its envelope was.
func TestIAMTokenIsAcceptedAsABearer(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	p := newIDP(t)
	src := register(t, p)
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	link(t, src.ID, "sub-of-user-2", u)

	req := httptest.NewRequest("GET", "/v1/user", nil)
	req.Header.Set("Authorization", "Bearer "+p.sign(t, p.url, "sub-of-user-2", time.Hour))
	store := &mockDataStore{}

	got, err := (&IAM{}).Verify(req, nil, store, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "a Bearer carrying an IAM token resolves its account")
	assert.Equal(t, u.ID, got.ID)

	// The scope is unchanged by the scheme: presenting it as a Bearer must not
	// buy more than presenting it to git did.
	assert.Equal(t, auth_model.AccessTokenScopeWriteRepository, store.GetData()["ApiTokenScope"])
	assert.Equal(t, IAMTokenMethodName, store.GetData()["LoginMethod"])
}

// TestIAMDeclinesABearerItDidNotSign leaves the rest of the chain its turn.
func TestIAMDeclinesABearerItDidNotSign(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	newIDP(t)

	req := httptest.NewRequest("GET", "/v1/user", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef01234567")
	got, err := (&IAM{}).Verify(req, nil, &mockDataStore{}, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "a Gitea token is not ours to answer")
}
