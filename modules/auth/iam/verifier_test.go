// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package iam

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/git/modules/json"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testKid      = "test-key"
	testAudience = "hanzo-git"
)

// issuer is a stand-in for Hanzo IAM: it publishes a discovery document and a key
// set, and counts what is read so caching can be observed.
type issuer struct {
	*httptest.Server
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey

	discoveryReads atomic.Int32
	keyReads       atomic.Int32

	name     string            // what the discovery document claims as the issuer, defaults to the server URL
	keysAt   string            // where the discovery document points, defaults to this server's /keys
	publish  func(*issuer) any // key set to publish, defaults to the RSA key
	keysFail atomic.Bool
	hold     chan struct{} // when set, /keys waits on it before answering
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	i := &issuer{rsaKey: rsaKey, ecKey: ecKey}
	i.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			i.discoveryReads.Add(1)
			write(w, map[string]string{"issuer": i.issuerName(), "jwks_uri": i.keysURL()})
		case "/elsewhere":
			http.Redirect(w, r, i.URL+"/keys", http.StatusFound)
		case "/keys":
			i.keyReads.Add(1)
			if i.hold != nil {
				<-i.hold
			}
			if i.keysFail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if i.publish != nil {
				write(w, i.publish(i))
				return
			}
			write(w, map[string]any{"keys": []any{rsaJWK(testKid, "RS256", &rsaKey.PublicKey)}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(i.Close)
	return i
}

func (i *issuer) issuerName() string {
	if i.name != "" {
		return i.name
	}
	return i.URL
}

func (i *issuer) keysURL() string {
	if i.keysAt != "" {
		return i.keysAt
	}
	return i.URL + "/keys"
}

// sign mints a token the way IAM would, so a test states only how it differs.
func (i *issuer) sign(t *testing.T, method jwt.SigningMethod, key any, edit func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":       i.URL,
		"sub":       "iam-subject",
		"aud":       testAudience,
		"tokenType": AccessToken,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(time.Hour).Unix(),
	}
	if edit != nil {
		edit(claims)
	}
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = testKid
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

func (i *issuer) valid(t *testing.T) string {
	t.Helper()
	return i.sign(t, jwt.SigningMethodRS256, i.rsaKey, nil)
}

func verifier(iss *issuer) *Verifier { return New(iss.URL, testAudience) }

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(v)
	_, _ = w.Write(body)
}

func rsaJWK(kid, alg string, k *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "use": "sig", "kid": kid, "alg": alg,
		"n": raw(k.N.Bytes()),
		"e": raw(big.NewInt(int64(k.E)).Bytes()),
	}
}

func ecJWK(kid, alg, crv string, k *ecdsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "EC", "use": "sig", "kid": kid, "alg": alg, "crv": crv,
		"x": raw(k.X.Bytes()),
		"y": raw(k.Y.Bytes()),
	}
}

func raw(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func subject(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, _ := claims["sub"].(string)
	return s
}

func TestVerifyAcceptsIssuedToken(t *testing.T) {
	iss := newIssuer(t)

	claims, err := verifier(iss).Verify(t.Context(), iss.valid(t))
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))
}

func TestVerifyAcceptsECKey(t *testing.T) {
	iss := newIssuer(t)
	iss.publish = func(i *issuer) any {
		return map[string]any{"keys": []any{ecJWK(testKid, "ES256", "P-256", &i.ecKey.PublicKey)}}
	}

	claims, err := verifier(iss).Verify(t.Context(), iss.sign(t, jwt.SigningMethodES256, iss.ecKey, nil))
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))
}

func TestVerifyRefusesForeignKey(t *testing.T) {
	iss := newIssuer(t)
	foreign, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Same key id, same algorithm, different key: only the signature differs.
	token := iss.sign(t, jwt.SigningMethodRS256, foreign, nil)

	claims, err := verifier(iss).Verify(t.Context(), token)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, jwt.ErrTokenSignatureInvalid)
	// The published key was fetched and matched, so this is the signature failing
	// rather than the algorithm list refusing the token earlier.
	assert.Equal(t, int32(1), iss.keyReads.Load())
	assert.ErrorContains(t, err, "verification error")
}

func TestVerifyRefusesUnsignedToken(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, nil)

	claims, err := verifier(iss).Verify(t.Context(), token)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "none")
	assert.Zero(t, iss.keyReads.Load(), "an unsigned token must be refused before any key is looked at")
}

func TestVerifyRefusesHMACOverThePublicKey(t *testing.T) {
	iss := newIssuer(t)
	// The classic confusion: present the verifier's own public key back to it as an
	// HMAC secret, so whoever can read the key set could mint tokens.
	public, err := x509.MarshalPKIXPublicKey(&iss.rsaKey.PublicKey)
	require.NoError(t, err)
	token := iss.sign(t, jwt.SigningMethodHS256, public, nil)

	claims, err := verifier(iss).Verify(t.Context(), token)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.Contains(t, err.Error(), "HS256")
	assert.Zero(t, iss.keyReads.Load(), "a symmetric algorithm must be refused before any key is looked at")
}

// TestKeyFuncTiesAlgorithmToKey covers the second refusal of a symmetric
// algorithm, the one inside the key lookup, which the parser's algorithm list
// keeps anything from reaching in practice.
func TestKeyFuncTiesAlgorithmToKey(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)
	_, err := v.Verify(t.Context(), iss.valid(t))
	require.NoError(t, err)

	keyFunc := v.keyFunc(t.Context())

	hmac := &jwt.Token{Method: jwt.SigningMethodHS256, Header: map[string]any{"kid": testKid}}
	_, err = keyFunc(hmac)
	require.ErrorContains(t, err, "key \""+testKid+"\" is for \"RS256\"")

	// A different asymmetric algorithm on the same key is refused too.
	other := &jwt.Token{Method: jwt.SigningMethodRS512, Header: map[string]any{"kid": testKid}}
	_, err = keyFunc(other)
	require.ErrorContains(t, err, "is for \"RS256\"")

	unnamed := &jwt.Token{Method: jwt.SigningMethodRS256, Header: map[string]any{}}
	_, err = keyFunc(unnamed)
	require.ErrorContains(t, err, "names no key")
}

func TestVerifyRefusesExpiredToken(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		c["exp"] = time.Now().Add(-time.Hour).Unix()
	})

	claims, err := verifier(iss).Verify(t.Context(), token)
	require.Error(t, err)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, jwt.ErrTokenExpired)
}

func TestVerifyRefusesTokenWithoutExpiry(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) { delete(c, "exp") })

	_, err := verifier(iss).Verify(t.Context(), token)
	assert.ErrorIs(t, err, jwt.ErrTokenRequiredClaimMissing)
}

func TestVerifyRefusesTokenNotYetValid(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
		c["nbf"] = time.Now().Add(time.Hour).Unix()
	})

	_, err := verifier(iss).Verify(t.Context(), token)
	assert.ErrorIs(t, err, jwt.ErrTokenNotValidYet)
}

func TestVerifyRefusesAnotherIssuer(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
		c["iss"] = "https://elsewhere.example"
	})

	claims, err := verifier(iss).Verify(t.Context(), token)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, ErrNotIAM, "a token from another issuer is not this issuer's to judge")
	assert.Zero(t, iss.discoveryReads.Load(), "another issuer's token must not reach the key cache")
}

// TestVerifyRefusesAnotherAudience is the one that keeps this forge from being a
// deputy for every other application the issuer serves.
func TestVerifyRefusesAnotherAudience(t *testing.T) {
	iss := newIssuer(t)

	for name, aud := range map[string]any{
		"another client": "some-other-app",
		"absent":         nil,
		"empty list":     []string{},
	} {
		t.Run(name, func(t *testing.T) {
			token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
				if aud == nil {
					delete(c, "aud")
					return
				}
				c["aud"] = aud
			})
			claims, err := verifier(iss).Verify(t.Context(), token)
			require.Error(t, err)
			assert.Nil(t, claims)
			assert.ErrorContains(t, err, "aud")
		})
	}

	// One of several audiences is enough, which is how an issuer names a resource
	// alongside the client.
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
		c["aud"] = []string{"some-other-app", testAudience}
	})
	_, err := verifier(iss).Verify(t.Context(), token)
	assert.NoError(t, err)
}

// TestVerifyRefusesIDToken guards the credential that is handed to browsers. It
// carries the same issuer, subject, audience and signature as an access token.
func TestVerifyRefusesIDToken(t *testing.T) {
	iss := newIssuer(t)

	for name, kind := range map[string]any{
		"id token": "id-token",
		"absent":   nil,
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) {
				if kind == nil {
					delete(c, "tokenType")
					return
				}
				c["tokenType"] = kind
			})
			claims, err := verifier(iss).Verify(t.Context(), token)
			assert.Nil(t, claims)
			assert.ErrorContains(t, err, "is not an access token")
		})
	}
}

func TestVerifyRefusesNonToken(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)

	for _, credential := range []string{"", "hunter2", "gto_pat_looking_string", "a.b", "a.b.c.d", "not.a.token"} {
		_, err := v.Verify(t.Context(), credential)
		assert.ErrorIs(t, err, ErrNotIAM, "credential %q", credential)
	}
	assert.Zero(t, iss.discoveryReads.Load())
}

func TestVerifyDeniesWhenKeysAreUnreachable(t *testing.T) {
	iss := newIssuer(t)
	iss.keysFail.Store(true)
	token := iss.valid(t)

	claims, err := verifier(iss).Verify(t.Context(), token)
	require.Error(t, err)
	assert.Nil(t, claims, "an unreachable issuer must deny, never admit")
	assert.ErrorIs(t, err, ErrIssuerUnreachable)
}

// TestVerifyKeepsSayingWhyWhileTheIssuerIsDown covers the refresh interval: the
// requests that follow a failed read are refused for the same reason as the one
// that made it, not told their key is unknown.
func TestVerifyKeepsSayingWhyWhileTheIssuerIsDown(t *testing.T) {
	iss := newIssuer(t)
	iss.keysFail.Store(true)
	v := verifier(iss)
	token := iss.valid(t)

	for range 5 {
		_, err := v.Verify(t.Context(), token)
		assert.ErrorIs(t, err, ErrIssuerUnreachable)
	}
	assert.Equal(t, int32(1), iss.keyReads.Load(), "a failed read is not retried within the refresh interval")

	// And it recovers on its own once the issuer answers again.
	iss.keysFail.Store(false)
	v.mu.Lock()
	v.attempted = time.Now().Add(-2 * refreshInterval)
	v.mu.Unlock()

	claims, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))
}

func TestVerifyDeniesWhenKeysGoStale(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)
	token := iss.valid(t)

	claims, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))

	// The issuer breaks and the cached set ages out: keys past their lifetime are
	// not fallen back on.
	iss.keysFail.Store(true)
	v.mu.Lock()
	v.fetched = time.Now().Add(-2 * keyTTL)
	v.attempted = time.Now().Add(-2 * refreshInterval)
	v.mu.Unlock()

	_, err = v.Verify(t.Context(), token)
	assert.ErrorIs(t, err, ErrIssuerUnreachable)
}

func TestVerifyRefusesMismatchedDiscovery(t *testing.T) {
	iss := newIssuer(t)
	iss.name = "https://elsewhere.example"

	_, err := verifier(iss).Verify(t.Context(), iss.valid(t))
	assert.ErrorContains(t, err, "discovery names issuer")
}

func TestVerifyAcceptsIssuerWithTrailingSlash(t *testing.T) {
	iss := newIssuer(t)
	iss.name = iss.URL + "/"

	claims, err := verifier(iss).Verify(t.Context(), iss.valid(t))
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))
}

// TestVerifyRefusesKeysOffTheIssuersOrigin keeps a discovery document from
// pointing the one thing that carries the whole check somewhere else.
func TestVerifyRefusesKeysOffTheIssuersOrigin(t *testing.T) {
	iss := newIssuer(t)
	elsewhere := newIssuer(t)
	iss.keysAt = elsewhere.URL + "/keys"

	_, err := verifier(iss).Verify(t.Context(), iss.valid(t))
	assert.ErrorContains(t, err, "not on the issuer's origin")
	assert.Zero(t, elsewhere.keyReads.Load())
}

func TestVerifyRefusesRedirectedKeys(t *testing.T) {
	iss := newIssuer(t)
	iss.keysAt = iss.URL + "/elsewhere"

	_, err := verifier(iss).Verify(t.Context(), iss.valid(t))
	assert.ErrorContains(t, err, "must answer directly")
}

func TestVerifyReadsKeysOnce(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)

	for range 5 {
		_, err := v.Verify(t.Context(), iss.valid(t))
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), iss.discoveryReads.Load())
	assert.Equal(t, int32(1), iss.keyReads.Load())
}

func TestVerifyUnderConcurrency(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)

	tokens := make([]string, 32)
	for i := range tokens {
		tokens[i] = iss.valid(t)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, token := range tokens {
		wg.Go(func() {
			<-start
			claims, err := v.Verify(t.Context(), token)
			assert.NoError(t, err)
			assert.Equal(t, "iam-subject", subject(t, claims))
		})
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), iss.keyReads.Load(), "a cold cache under load must read the key set once")
}

// TestVerifyDoesNotWaitOnAnInFlightRead is the reason the cache and the read take
// different locks: a slow issuer must not stall requests whose key is in hand.
func TestVerifyDoesNotWaitOnAnInFlightRead(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)

	known := iss.valid(t)
	_, err := v.Verify(t.Context(), known)
	require.NoError(t, err)

	// The set stays current, so the known key is a cache hit, but the last read is
	// old enough that an unknown key id starts a new one. That read then blocks
	// until this test lets it finish.
	release := make(chan struct{})
	iss.hold = release
	v.mu.Lock()
	v.attempted = time.Now().Add(-2 * refreshInterval)
	v.mu.Unlock()

	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		unknown := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss.URL, "sub": "iam-subject", "aud": testAudience, "tokenType": AccessToken,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		})
		unknown.Header["kid"] = "no-such-key"
		signed, _ := unknown.SignedString(iss.rsaKey)
		_, _ = v.Verify(t.Context(), signed)
	}()

	// The blocked read is holding the fetch lock; a token whose key is cached must
	// still go straight through.
	done := make(chan error, 1)
	go func() {
		_, err := v.Verify(t.Context(), known)
		done <- err
	}()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a cached key waited on an in-flight key read")
	}

	close(release)
	<-blocked
}

func TestVerifyRateLimitsRefresh(t *testing.T) {
	iss := newIssuer(t)
	v := verifier(iss)

	unknown := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": iss.URL, "sub": "iam-subject", "aud": testAudience, "tokenType": AccessToken,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	unknown.Header["kid"] = "no-such-key"
	signed, err := unknown.SignedString(iss.rsaKey)
	require.NoError(t, err)

	for range 10 {
		_, err := v.Verify(t.Context(), signed)
		assert.ErrorContains(t, err, `no key "no-such-key"`)
	}
	assert.Equal(t, int32(1), iss.keyReads.Load(), "unknown key ids must not drive traffic at the issuer")
}

func TestVerifyRefusesSubjectlessToken(t *testing.T) {
	iss := newIssuer(t)
	token := iss.sign(t, jwt.SigningMethodRS256, iss.rsaKey, func(c jwt.MapClaims) { delete(c, "sub") })

	_, err := verifier(iss).Verify(t.Context(), token)
	assert.ErrorContains(t, err, "carries no subject")
}

func TestPublicKeyRefusesWeakAndUnknownKeys(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	_, _, err = publicKey("RSA", raw(small.N.Bytes()), raw(big.NewInt(int64(small.E)).Bytes()), "", "", "")
	assert.ErrorContains(t, err, "1024 bits")

	_, _, err = publicKey("oct", "", "", "", "", "")
	assert.ErrorContains(t, err, "unknown key type")

	_, _, err = publicKey("EC", "", "", "P-224", "AA", "AA")
	assert.ErrorContains(t, err, "unknown curve")
}

func TestFetchSkipsUnusableKeys(t *testing.T) {
	iss := newIssuer(t)
	iss.publish = func(i *issuer) any {
		return map[string]any{"keys": []any{
			map[string]string{"kty": "oct", "kid": "symmetric", "alg": "HS256", "k": "c2VjcmV0"},
			map[string]string{
				"kty": "RSA", "kid": "encryption", "use": "enc", "alg": "RS256",
				"n": raw(i.rsaKey.N.Bytes()), "e": raw(big.NewInt(int64(i.rsaKey.E)).Bytes()),
			},
			// No "alg": the default for the key type is assumed.
			map[string]string{
				"kty": "RSA", "kid": testKid,
				"n": raw(i.rsaKey.N.Bytes()), "e": raw(big.NewInt(int64(i.rsaKey.E)).Bytes()),
			},
		}}
	}

	v := verifier(iss)
	claims, err := v.Verify(t.Context(), iss.valid(t))
	require.NoError(t, err)
	assert.Equal(t, "iam-subject", subject(t, claims))

	v.mu.Lock()
	defer v.mu.Unlock()
	assert.Len(t, v.keys, 1)
	assert.Equal(t, "RS256", v.keys[testKid].alg)
}
