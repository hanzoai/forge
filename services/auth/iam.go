// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	auth_model "github.com/hanzoai/git/models/auth"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/auth/httpauth"
	"github.com/hanzoai/git/modules/auth/iam"
	"github.com/hanzoai/git/modules/container"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/services/auth/source/oauth2"

	"github.com/golang-jwt/jwt/v5"
)

var _ Method = &IAM{}

// IAMMethodName is the constant name of the Hanzo IAM authentication method
const IAMMethodName = "iam"

// IAM authenticates a request carrying a Hanzo IAM access token, sent either as
// the password of a basic credential, which is how git over HTTP carries it, or
// as a bearer token. The token names an IAM identity, and that identity is
// resolved to the forge account already linked to it by the OAuth2 login source
// used for web sign-in. No account is created and no privilege is read from the
// token: it says who you are, the forge says what you may do.
//
// The method does nothing at all until an issuer is configured.
type IAM struct{}

// Name represents the name of auth method
func (m *IAM) Name() string {
	return IAMMethodName
}

func (m *IAM) Verify(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore) (*user_model.User, error) {
	verifier := iamVerifier()
	if verifier == nil {
		return nil, nil //nolint:nilnil // no issuer configured, the auth method is not applicable
	}

	credential := iamCredential(req)
	if credential == "" {
		return nil, nil //nolint:nilnil // the auth method is not applicable
	}

	claims, err := verifier.Verify(req.Context(), credential)
	if err != nil {
		if errors.Is(err, iam.ErrNotIAM) {
			return nil, nil //nolint:nilnil // some other kind of credential, let the other methods have it
		}
		if errors.Is(err, iam.ErrIssuerUnreachable) {
			// Every token is being refused, not just this one.
			log.Error("IAM Authorization: %v", err)
		} else {
			log.Debug("IAM Authorization: %v", err)
		}
		return nil, ErrUserAuthMessage("invalid Hanzo IAM token")
	}

	u, err := iamUser(req.Context(), claims)
	if err != nil {
		return nil, err
	}

	store.GetData()["LoginMethod"] = IAMMethodName
	store.GetData()["IsApiToken"] = true
	store.GetData()["ApiTokenScope"] = iamScope(claims)
	log.Trace("IAM Authorization: Logged in user %-v", u)
	return u, nil
}

// iamCredential returns the credential to check. Git over HTTP sends it as the
// basic password; some clients send it as the basic username instead, the same
// two places Basic already looks.
func iamCredential(req *http.Request) string {
	parsed, ok := httpauth.ParseAuthorizationHeader(req.Header.Get("Authorization"))
	if !ok {
		return ""
	}
	switch {
	case parsed.BearerToken != nil:
		return parsed.BearerToken.Token
	case parsed.BasicAuth != nil:
		if parsed.BasicAuth.Password != "" && parsed.BasicAuth.Password != "x-oauth-basic" {
			return parsed.BasicAuth.Password
		}
		return parsed.BasicAuth.Username
	}
	return ""
}

// iamScope is what the token itself says it may do. An IAM app that names forge
// scopes gets exactly those; one that names none is a token minted for this forge
// by someone who already holds the account, which is what signing in with a
// password amounts to, and it carries the same reach.
func iamScope(claims jwt.MapClaims) auth_model.AccessTokenScope {
	granted, _ := claims["scope"].(string)

	var named []string
	for one := range strings.FieldsSeq(granted) {
		if _, err := auth_model.AccessTokenScope(one).Normalize(); err == nil {
			named = append(named, one)
		}
	}
	if len(named) == 0 {
		return auth_model.AccessTokenScopeAll
	}
	scope, err := auth_model.AccessTokenScope(strings.Join(named, ",")).Normalize()
	if err != nil {
		return auth_model.AccessTokenScopeAll
	}
	return scope
}

// iamUser resolves the forge account linked to a verified token. The lookups
// mirror the OAuth2 sign-in handler, so a token reaches exactly the account that
// signing in through the browser reaches, under the same conditions.
func iamUser(ctx context.Context, claims jwt.MapClaims) (*user_model.User, error) {
	source, err := auth_model.GetActiveOAuth2SourceByAuthName(ctx, setting.IAM.LoginSource)
	if err != nil {
		return nil, err
	}
	if cfg, ok := source.Cfg.(*oauth2.Source); ok {
		if err := iamRequiredClaim(cfg, claims); err != nil {
			return nil, err
		}
	}

	subject, _ := claims["sub"].(string)
	u, has, err := user_model.GetIndividualUserByLoginSource(ctx, auth_model.OAuth2, source.ID, subject)
	if err != nil {
		return nil, err
	}
	if !has {
		link, linked, err := user_model.GetExternalLogin(ctx, source.ID, subject)
		if err != nil {
			return nil, err
		}
		if !linked {
			return nil, ErrUserAuthMessage("no account is linked to this Hanzo IAM identity, sign in through the web interface once to link it")
		}
		if u, err = user_model.GetUserByID(ctx, link.UserID); err != nil {
			return nil, err
		}
	}

	if !u.IsIndividual() || !u.IsActive || u.ProhibitLogin {
		return nil, ErrUserAuthMessage("this account cannot sign in")
	}
	return u, nil
}

// iamRequiredClaim applies the login source's claim restriction, the one the web
// sign-in handler applies, so a token cannot outlive the group membership that
// browser sign-in depends on.
func iamRequiredClaim(cfg *oauth2.Source, claims jwt.MapClaims) error {
	if cfg.RequiredClaimName == "" {
		return nil
	}
	value, has := claims[cfg.RequiredClaimName]
	if !has {
		return ErrUserAuthMessage(fmt.Sprintf("this Hanzo IAM identity carries no %q claim", cfg.RequiredClaimName))
	}
	if cfg.RequiredClaimValue != "" && !claimValues(value).Contains(cfg.RequiredClaimValue) {
		return ErrUserAuthMessage(fmt.Sprintf("this Hanzo IAM identity is not %q", cfg.RequiredClaimValue))
	}
	return nil
}

// claimValues reads a claim that may be one value or many, the way the web
// sign-in handler reads it.
func claimValues(value any) container.Set[string] {
	switch v := value.(type) {
	case []string:
		return container.SetOf(v...)
	case []any:
		values := make([]string, 0, len(v))
		for _, one := range v {
			values = append(values, fmt.Sprintf("%s", one))
		}
		return container.SetOf(values...)
	default:
		return container.SetOf(strings.Split(fmt.Sprintf("%s", v), ",")...)
	}
}

// iamState holds the verifier between requests, so the issuer's signing keys are
// fetched once rather than per request. It is rebuilt when the configuration it
// was built from changes.
var iamState struct {
	sync.Mutex
	issuer, audience string
	verifier         *iam.Verifier
}

func iamVerifier() *iam.Verifier {
	iamState.Lock()
	defer iamState.Unlock()

	if setting.IAM.Issuer == "" {
		iamState.issuer, iamState.audience, iamState.verifier = "", "", nil
		return nil
	}
	if iamState.verifier == nil || iamState.issuer != setting.IAM.Issuer || iamState.audience != setting.IAM.Audience {
		iamState.issuer, iamState.audience = setting.IAM.Issuer, setting.IAM.Audience
		iamState.verifier = iam.New(setting.IAM.Issuer, setting.IAM.Audience)
	}
	return iamState.verifier
}
