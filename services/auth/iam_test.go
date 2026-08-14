// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/auth/iam"
	"github.com/hanzoai/git/modules/json"
	"github.com/hanzoai/git/modules/reqctx"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/services/auth/source/oauth2"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	iamSubject     = "hanzo/user2"
	iamLoginSource = "hanzo"
	iamAudience    = "hanzo-git"
)

// testIssuer is a stand-in for Hanzo IAM, publishing a discovery document and one
// signing key, and counting how often it is read.
type testIssuer struct {
	*httptest.Server
	key   *rsa.PrivateKey
	reads atomic.Int32
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	iss := &testIssuer{key: key}
	iss.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iss.reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			body = map[string]string{"issuer": iss.URL, "jwks_uri": iss.URL + "/keys"}
		case "/keys":
			body = map[string]any{"keys": []any{map[string]string{
				"kty": "RSA", "use": "sig", "kid": "iam", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		out, _ := json.Marshal(body)
		_, _ = w.Write(out)
	}))
	t.Cleanup(iss.Close)
	return iss
}

func (iss *testIssuer) token(t *testing.T, edit func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":       iss.URL,
		"sub":       iamSubject,
		"aud":       iamAudience,
		"tokenType": iam.AccessToken,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	if edit != nil {
		edit(claims)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "iam"
	signed, err := token.SignedString(iss.key)
	require.NoError(t, err)
	return signed
}

// gitRequest is what git over HTTP sends: the token as the basic password.
func gitRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/hanzo/git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("hanzo", token)
	return req
}

// useIAM points the settings at a stand-in issuer for the duration of a test.
func useIAM(t *testing.T, iss *testIssuer) {
	t.Helper()
	t.Cleanup(test.MockVariableValue(&setting.IAM.Issuer, iss.URL))
	t.Cleanup(test.MockVariableValue(&setting.IAM.LoginSource, iamLoginSource))
	t.Cleanup(test.MockVariableValue(&setting.IAM.Audience, iamAudience))
}

// loginSource creates the OAuth2 login source the forge signs in with.
func loginSource(t *testing.T, cfg *oauth2.Source) int64 {
	t.Helper()
	if cfg == nil {
		cfg = &oauth2.Source{Provider: "openidConnect"}
	}
	source := &auth_model.Source{
		Type:     auth_model.OAuth2,
		Name:     iamLoginSource,
		IsActive: true,
		Cfg:      cfg,
	}
	require.NoError(t, db.Insert(t.Context(), source))
	return source.ID
}

func link(t *testing.T, sourceID, userID int64) {
	t.Helper()
	require.NoError(t, db.Insert(t.Context(), &user_model.ExternalLoginUser{
		ExternalID:    iamSubject,
		UserID:        userID,
		LoginSourceID: sourceID,
		Provider:      "openidConnect",
	}))
}

func TestIAMIsInertWithoutIssuer(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	sourceID := loginSource(t, nil)
	link(t, sourceID, 2)

	t.Cleanup(test.MockVariableValue(&setting.IAM.Issuer, ""))
	t.Cleanup(test.MockVariableValue(&setting.IAM.LoginSource, ""))
	t.Cleanup(test.MockVariableValue(&setting.IAM.Audience, ""))

	store := make(reqctx.ContextData)
	u, err := (&IAM{}).Verify(gitRequest(iss.token(t, nil)), nil, store, nil)

	require.NoError(t, err)
	assert.Nil(t, u)
	assert.Empty(t, store)
	assert.Zero(t, iss.reads.Load(), "with no issuer configured nothing may be read from one")
}

func TestIAMAuthenticatesLinkedUser(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	link(t, loginSource(t, nil), 2)

	store := make(reqctx.ContextData)
	u, err := (&IAM{}).Verify(gitRequest(iss.token(t, nil)), nil, store, nil)

	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, int64(2), u.ID)
	assert.Equal(t, IAMMethodName, store["LoginMethod"])
	assert.Equal(t, true, store["IsApiToken"])
	assert.Equal(t, auth_model.AccessTokenScopeAll, GetAccessScope(store))
}

func TestIAMAuthenticatesUserBoundToSource(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	sourceID := loginSource(t, nil)

	// A user whose own login source is the OAuth2 one, which is the first place the
	// sign-in handler looks.
	_, err := db.GetEngine(t.Context()).ID(2).Cols("login_type", "login_source", "login_name").
		Update(&user_model.User{LoginType: auth_model.OAuth2, LoginSource: sourceID, LoginName: iamSubject})
	require.NoError(t, err)

	u, err := (&IAM{}).Verify(gitRequest(iss.token(t, nil)), nil, make(reqctx.ContextData), nil)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, int64(2), u.ID)
}

func TestIAMTakesScopeFromTheToken(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	link(t, loginSource(t, nil), 2)

	cases := map[string]auth_model.AccessTokenScope{
		"openid profile email":              auth_model.AccessTokenScopeAll,
		"":                                  auth_model.AccessTokenScopeAll,
		"openid write:repository":           auth_model.AccessTokenScopeWriteRepository,
		"read:repository read:package":      "read:package,read:repository",
		"write:repository openid read:user": "write:repository,read:user",
	}
	for granted, want := range cases {
		t.Run(granted, func(t *testing.T) {
			store := make(reqctx.ContextData)
			token := iss.token(t, func(c jwt.MapClaims) { c["scope"] = granted })
			u, err := (&IAM{}).Verify(gitRequest(token), nil, store, nil)
			require.NoError(t, err)
			require.NotNil(t, u)
			assert.Equal(t, want, GetAccessScope(store))
		})
	}
}

func TestIAMRefusesUnlinkedIdentity(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	loginSource(t, nil)

	store := make(reqctx.ContextData)
	u, err := (&IAM{}).Verify(gitRequest(iss.token(t, nil)), nil, store, nil)
	require.Error(t, err)
	assert.Nil(t, u, "a valid token for an unlinked identity creates nothing")
	assert.Empty(t, store, "a refused token leaves nothing behind")

	message, ok := ErrAsUserAuthMessage(err)
	assert.True(t, ok)
	assert.Contains(t, message, "no account is linked")
}

func TestIAMRefusesAccountsThatCannotSignIn(t *testing.T) {
	// user 2 is an ordinary user, 3 is an organization, 9 is an individual whose
	// account was never activated.
	cases := map[string]struct {
		userID int64
		set    func(t *testing.T)
	}{
		"prohibited": {userID: 2, set: func(t *testing.T) {
			_, err := db.GetEngine(t.Context()).ID(2).Cols("prohibit_login").Update(&user_model.User{ProhibitLogin: true})
			require.NoError(t, err)
		}},
		"deactivated": {userID: 2, set: func(t *testing.T) {
			_, err := db.GetEngine(t.Context()).ID(2).Cols("is_active").Update(&user_model.User{IsActive: false})
			require.NoError(t, err)
		}},
		"organization":    {userID: 3},
		"never activated": {userID: 9},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			iss := newTestIssuer(t)
			useIAM(t, iss)
			link(t, loginSource(t, nil), c.userID)
			if c.set != nil {
				c.set(t)
			}

			store := make(reqctx.ContextData)
			u, err := (&IAM{}).Verify(gitRequest(iss.token(t, nil)), nil, store, nil)
			require.Error(t, err)
			assert.Nil(t, u)
			assert.Empty(t, store)
		})
	}
}

func TestIAMAppliesTheSourcesRequiredClaim(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	sourceID := loginSource(t, &oauth2.Source{
		Provider:           "openidConnect",
		RequiredClaimName:  "groups",
		RequiredClaimValue: "engineering",
	})
	link(t, sourceID, 2)

	member := iss.token(t, func(c jwt.MapClaims) { c["groups"] = []string{"design", "engineering"} })
	u, err := (&IAM{}).Verify(gitRequest(member), nil, make(reqctx.ContextData), nil)
	require.NoError(t, err)
	require.NotNil(t, u)

	// The claim is how the forge learns someone is still on the team, so a token
	// that has lost it must stop working, exactly as browser sign-in does.
	for name, edit := range map[string]func(jwt.MapClaims){
		"left the group": func(c jwt.MapClaims) { c["groups"] = []string{"design"} },
		"no claim":       func(c jwt.MapClaims) {},
	} {
		t.Run(name, func(t *testing.T) {
			u, err := (&IAM{}).Verify(gitRequest(iss.token(t, edit)), nil, make(reqctx.ContextData), nil)
			require.Error(t, err)
			assert.Nil(t, u)
		})
	}
}

func TestIAMLeavesOtherCredentialsAlone(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	link(t, loginSource(t, nil), 2)

	elsewhere := newTestIssuer(t)
	foreign := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": elsewhere.URL, "sub": iamSubject, "aud": iamAudience, "tokenType": iam.AccessToken,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	foreign.Header["kid"] = "iam"
	foreignToken, err := foreign.SignedString(elsewhere.key)
	require.NoError(t, err)

	for name, req := range map[string]*http.Request{
		"password":        gitRequest("password"),
		"access token":    gitRequest("gto_ba7c05a4a3e3b3d8e5b6e4b1d2c3a4b5c6d7e8f9"),
		"another issuer":  gitRequest(foreignToken),
		"no header":       httptest.NewRequest(http.MethodGet, "/", nil),
		"bearer password": httptest.NewRequest(http.MethodGet, "/", nil),
	} {
		t.Run(name, func(t *testing.T) {
			store := make(reqctx.ContextData)
			u, err := (&IAM{}).Verify(req, nil, store, nil)
			require.NoError(t, err)
			assert.Nil(t, u)
			assert.Empty(t, store)
		})
	}
	assert.Zero(t, iss.reads.Load(), "credentials that are not this issuer's must not reach it")
}

func TestIAMDeniesWhenIssuerIsUnreachable(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	iss := newTestIssuer(t)
	useIAM(t, iss)
	link(t, loginSource(t, nil), 2)
	token := iss.token(t, nil)
	iss.Close()

	store := make(reqctx.ContextData)
	u, err := (&IAM{}).Verify(gitRequest(token), nil, store, nil)
	require.Error(t, err)
	assert.Nil(t, u, "an unreachable issuer must deny, never admit")
	assert.Empty(t, store)
}

func TestIAMCredentialFromRequest(t *testing.T) {
	bearer := httptest.NewRequest(http.MethodGet, "/", nil)
	bearer.Header.Set("Authorization", "Bearer a.b.c")
	assert.Equal(t, "a.b.c", iamCredential(bearer))

	assert.Equal(t, "secret", iamCredential(gitRequest("secret")))

	// Clients that put the token in the username, the way Basic already reads them.
	asUsername := httptest.NewRequest(http.MethodGet, "/", nil)
	asUsername.SetBasicAuth("a.b.c", "x-oauth-basic")
	assert.Equal(t, "a.b.c", iamCredential(asUsername))

	assert.Empty(t, iamCredential(httptest.NewRequest(http.MethodGet, "/", nil)))
}
