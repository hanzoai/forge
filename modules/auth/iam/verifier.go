// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// Package iam verifies access tokens minted by a Hanzo IAM issuer.
package iam

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/git/modules/json"
	"github.com/hanzoai/git/modules/proxy"

	"github.com/golang-jwt/jwt/v5"
)

// ErrNotIAM says the credential is not a token from this issuer, so a caller can
// pass it on to another authentication method instead of failing the request.
var ErrNotIAM = errors.New("not a Hanzo IAM token")

// ErrIssuerUnreachable says the issuer's signing keys could not be read. Tokens
// are refused for as long as that lasts, so it is worth an operator's attention.
var ErrIssuerUnreachable = errors.New("cannot read Hanzo IAM signing keys")

const (
	// AccessToken is the tokenType an issuer stamps on a token meant to be
	// presented as a credential. An id_token carries the same issuer, subject and
	// signature but is a client-side artifact, so it is refused here.
	AccessToken = "access-token"

	// keyTTL bounds how long a fetched key set stays usable. Once it lapses the
	// keys are refetched; if that fails, tokens are refused rather than checked
	// against keys the issuer may already have withdrawn.
	keyTTL = time.Hour

	// refreshInterval is the shortest gap between two fetches, so unknown key ids
	// in incoming tokens cannot be used to drive traffic at the issuer.
	refreshInterval = 30 * time.Second

	fetchTimeout = 10 * time.Second

	// maxDocument caps what is read from the issuer's endpoints.
	maxDocument = 1 << 20

	// clockSkew is how far a token's time bounds may be off from ours.
	clockSkew = 30 * time.Second
)

// signingMethods are the asymmetric algorithms this verifier will consider. It
// deliberately holds no HMAC algorithm and no "none": a token is checked against
// a published public key or it is not checked at all. The exact algorithm still
// has to match the one its key declares, see keyFunc.
var signingMethods = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}

// defaultAlg names the algorithm assumed for a published key that omits the
// optional "alg" member, so key rotation does not depend on it being present.
var defaultAlg = map[string]string{"RSA": "RS256", "P-256": "ES256", "P-384": "ES384", "P-521": "ES512"}

// key is one signing key the issuer publishes, with the algorithm it is for.
type key struct {
	alg    string
	public any
}

// Verifier checks tokens against the keys an issuer publishes, holding those
// keys between requests.
type Verifier struct {
	issuer   string
	audience string
	client   *http.Client

	// fetching is held across the network read, so only one is ever in flight.
	// Reading the cache takes mu alone and never waits behind it.
	fetching sync.Mutex

	mu        sync.Mutex
	keys      map[string]key
	fetched   time.Time
	attempted time.Time
	failure   error
}

// New returns a verifier for tokens minted by issuer and carrying audience in
// their aud claim. Both are required: without the audience any token the issuer
// minted for any application would be accepted here.
func New(issuer, audience string) *Verifier {
	return &Verifier{
		issuer:   strings.TrimSuffix(issuer, "/"),
		audience: audience,
		client: &http.Client{
			Timeout:   fetchTimeout,
			Transport: &http.Transport{Proxy: proxy.Proxy()},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("issuer endpoints must answer directly rather than redirect")
			},
		},
	}
}

// Verify checks a token and returns its claims.
//
// The token must be signed by a key the issuer publishes, using the algorithm
// that key is for; must name the issuer and this verifier's audience; must be
// within its exp bound, and within nbf and iat where it carries them; and must
// be an access token rather than an id_token. Anything else returns an error and
// no claims. ErrNotIAM means the credential belongs to another issuer or is not
// a token at all.
func (v *Verifier) Verify(ctx context.Context, token string) (jwt.MapClaims, error) {
	if strings.Count(token, ".") != 2 {
		return nil, ErrNotIAM
	}

	// Read the issuer without trusting it, only to tell whether this token is ours.
	// It is checked again below against the verified claims, so nothing rests on
	// what is read here; it just keeps other issuers' tokens from reaching the key
	// cache.
	var unverified jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &unverified); err != nil {
		return nil, ErrNotIAM
	}
	if unverified.Issuer != v.issuer {
		return nil, ErrNotIAM
	}

	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods(signingMethods),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(clockSkew),
	)
	if _, err := parser.ParseWithClaims(token, &claims, v.keyFunc(ctx)); err != nil {
		return nil, fmt.Errorf("iam: token refused: %w", err)
	}

	// An id_token names the same subject under the same signature, but it is minted
	// for a browser to read, not for a client to present.
	if kind, _ := claims["tokenType"].(string); kind != AccessToken {
		return nil, fmt.Errorf("iam: %q is not an access token", kind)
	}
	if subject, _ := claims["sub"].(string); subject == "" {
		return nil, errors.New("iam: token carries no subject")
	}
	return claims, nil
}

// keyFunc resolves the key named by the token header. Beyond finding the key it
// ties the token's algorithm to that key twice over: to the algorithm the key
// declares, and to the key's own type. So a token signed with an HMAC over the
// public key bytes has nothing to match, and neither has an unsigned one.
func (v *Verifier) keyFunc(ctx context.Context) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		k, err := v.key(ctx, kid)
		if err != nil {
			return nil, err
		}
		alg := token.Method.Alg()
		if alg != k.alg {
			return nil, fmt.Errorf("iam: token uses %q but key %q is for %q", alg, kid, k.alg)
		}
		switch k.public.(type) {
		case *rsa.PublicKey:
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("iam: key %q is RSA, token algorithm %q is not", kid, alg)
			}
		case *ecdsa.PublicKey:
			if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
				return nil, fmt.Errorf("iam: key %q is EC, token algorithm %q is not", kid, alg)
			}
		default:
			return nil, fmt.Errorf("iam: key %q has an unusable type", kid)
		}
		return k.public, nil
	}
}

// cached returns the published key with the given id while the set is current.
// It is the only path a request takes when the keys are in hand, and it never
// waits on the network.
func (v *Verifier) cached(kid string) (key, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Since(v.fetched) >= keyTTL {
		return key{}, false
	}
	k, ok := v.keys[kid]
	return k, ok
}

// key returns the published key with the given id, reading the set again when it
// is unknown or past its lifetime. A read that fails leaves the caller without a
// key, and its error stands for the whole refresh interval, so an unreachable
// issuer refuses tokens and says why rather than reporting a missing key.
func (v *Verifier) key(ctx context.Context, kid string) (key, error) {
	if kid == "" {
		return key{}, errors.New("iam: token names no key")
	}
	if k, ok := v.cached(kid); ok {
		return k, nil
	}

	v.fetching.Lock()
	defer v.fetching.Unlock()

	// Another request may have read the set while this one waited its turn.
	if k, ok := v.cached(kid); ok {
		return k, nil
	}

	v.mu.Lock()
	recent, failure := time.Since(v.attempted) < refreshInterval, v.failure
	v.mu.Unlock()
	if recent {
		if failure != nil {
			return key{}, failure
		}
		return key{}, fmt.Errorf("iam: no key %q", kid)
	}

	keys, err := v.fetch(ctx)

	v.mu.Lock()
	v.attempted, v.failure = time.Now(), nil
	if err != nil {
		v.failure = fmt.Errorf("iam: %w: %w", ErrIssuerUnreachable, err)
	} else {
		v.keys, v.fetched = keys, time.Now()
	}
	failure = v.failure
	k, ok := v.keys[kid]
	v.mu.Unlock()

	if failure != nil {
		return key{}, failure
	}
	if !ok {
		return key{}, fmt.Errorf("iam: no key %q", kid)
	}
	return k, nil
}

// fetch reads the issuer's key set, finding it through OpenID Connect discovery
// so the location comes from the issuer itself.
func (v *Verifier) fetch(ctx context.Context) (map[string]key, error) {
	// The keys outlive the request that asked for them, and the refresh interval
	// means a client that hangs up mid-read would otherwise leave the next half
	// minute of requests with nothing to check against.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()

	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.get(ctx, v.issuer+"/.well-known/openid-configuration", &discovery); err != nil {
		return nil, err
	}
	if strings.TrimSuffix(discovery.Issuer, "/") != v.issuer {
		return nil, fmt.Errorf("discovery names issuer %q, want %q", discovery.Issuer, v.issuer)
	}
	// The keys carry the whole weight of the check, so they have to come from the
	// issuer's own origin - the one the configuration named and TLS vouches for.
	if err := sameOrigin(discovery.JWKSURI, v.issuer); err != nil {
		return nil, fmt.Errorf("jwks_uri %q: %w", discovery.JWKSURI, err)
	}

	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := v.get(ctx, discovery.JWKSURI, &set); err != nil {
		return nil, err
	}

	keys := make(map[string]key, len(set.Keys))
	for _, published := range set.Keys {
		if published.Kid == "" || (published.Use != "" && published.Use != "sig") {
			continue
		}
		public, group, err := publicKey(published.Kty, published.N, published.E, published.Crv, published.X, published.Y)
		if err != nil {
			continue // a key this build cannot use; others in the set may still work
		}
		alg := published.Alg
		if alg == "" {
			alg = defaultAlg[group]
		}
		if alg == "" {
			continue
		}
		keys[published.Kid] = key{alg: alg, public: public}
	}
	if len(keys) == 0 {
		return nil, errors.New("issuer publishes no usable signing key")
	}
	return keys, nil
}

func (v *Verifier) get(ctx context.Context, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", address, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxDocument)).Decode(out)
}

// sameOrigin reports whether address shares a scheme and host with origin.
func sameOrigin(address, origin string) error {
	a, err := url.Parse(address)
	if err != nil {
		return err
	}
	o, err := url.Parse(origin)
	if err != nil {
		return err
	}
	if a.Scheme != o.Scheme || a.Host != o.Host {
		return fmt.Errorf("not on the issuer's origin %s://%s", o.Scheme, o.Host)
	}
	return nil
}

// publicKey builds a key from its published form and reports the group it
// belongs to, which is the key type for RSA and the curve for EC.
func publicKey(kty, n, e, crv, x, y string) (any, string, error) {
	switch kty {
	case "RSA":
		modulus, err := decode(n)
		if err != nil {
			return nil, "", err
		}
		exponent, err := decode(e)
		if err != nil {
			return nil, "", err
		}
		exp := new(big.Int).SetBytes(exponent)
		if !exp.IsInt64() || exp.Int64() < 3 {
			return nil, "", errors.New("rsa exponent out of range")
		}
		public := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exp.Int64())}
		if public.N.BitLen() < 2048 {
			return nil, "", fmt.Errorf("rsa key is only %d bits", public.N.BitLen())
		}
		return public, "RSA", nil

	case "EC":
		var curve elliptic.Curve
		switch crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, "", fmt.Errorf("unknown curve %q", crv)
		}
		abscissa, err := decode(x)
		if err != nil {
			return nil, "", err
		}
		ordinate, err := decode(y)
		if err != nil {
			return nil, "", err
		}
		public := &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(abscissa),
			Y:     new(big.Int).SetBytes(ordinate),
		}
		return public, crv, nil
	}
	return nil, "", fmt.Errorf("unknown key type %q", kty)
}

func decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty key value")
	}
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
