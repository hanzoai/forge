// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"net/http"

	actions_model "github.com/hanzoai/git/models/actions"
	auth_model "github.com/hanzoai/git/models/auth"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/auth/httpauth"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
)

// Ensure the struct implements the interface.
var (
	_ Method = &Basic{}
)

// BasicMethodName is the constant name of the basic authentication method
const (
	BasicMethodName       = "basic"
	AccessTokenMethodName = "access_token"
	OAuth2TokenMethodName = "oauth2_token"
	ActionTokenMethodName = "action_token"
)

// Basic implements the Auth interface and authenticates requests (API requests
// only) by looking for Basic authentication data or "x-oauth-basic" token in the "Authorization"
// header.
type Basic struct{}

// Name represents the name of auth method
func (b *Basic) Name() string {
	return BasicMethodName
}

func (b *Basic) parseAuthBasic(req *http.Request) (ret struct{ authToken, uname, passwd string }) {
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return ret
	}
	parsed, ok := httpauth.ParseAuthorizationHeader(authHeader)
	if !ok || parsed.BasicAuth == nil {
		return ret
	}
	uname, passwd := parsed.BasicAuth.Username, parsed.BasicAuth.Password

	// Check if username or password is a token
	isUsernameToken := len(passwd) == 0 || passwd == "x-oauth-basic"
	// Assume username is token
	authToken := uname
	if !isUsernameToken {
		log.Trace("Basic Authorization: Attempting login for: %s", uname)
		// Assume password is token
		authToken = passwd
	} else {
		log.Trace("Basic Authorization: Attempting login with username as token")
	}
	ret.authToken, ret.uname, ret.passwd = authToken, uname, passwd
	return ret
}

// VerifyAuthToken only the access token provided as parameter, used by other auth methods that want to reuse access token verification logic
func (b *Basic) VerifyAuthToken(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore, authToken string) (*user_model.User, error) {
	// IAM issues the JWT behind every access token and API key, so a credential
	// this instance minted for itself is a second authority for an identity that
	// already has one — a second lifetime to track and a second thing to revoke.
	// The OAuth2 access token and the personal access token that were read here
	// are gone; what a caller presents is an IAM token or nothing.

	// A task token is not a user credential and is not IAM's to issue: the
	// Actions protocol mints it per job, scoped to that job, and hands it to the
	// runner that is already executing it. Reading it here is upstream's
	// protocol, not an identity this instance is asserting.
	task, err := actions_model.GetRunningTaskByToken(req.Context(), authToken)
	if err == nil && task != nil {
		log.Trace("Basic Authorization: Valid AccessToken for task[%d]", task.ID)
		store.GetData()["LoginMethod"] = ActionTokenMethodName
		return user_model.NewActionsUserWithTaskID(task.ID), nil
	}

	// check a Hanzo IAM access token — the credential a user signed in with, which
	// on an externally-authenticated instance is the only one they hold without
	// first going and making a second. Asked LAST, after every local lookup has
	// declined, so a token this instance issued itself is never sent to a verifier
	// that would only reject it. Repository scope: see iam.go.
	if u := iamUser(req.Context(), authToken); u != nil {
		log.Trace("Basic Authorization: Valid IAM token for user[%d]", u.ID)
		setIAMTokenScope(store)
		return u, nil
	}
	return nil, nil //nolint:nilnil // the auth method is not applicable
}

// Verify extracts and validates Basic data (username and password/token) from the
// "Authorization" header of the request and returns the corresponding user object for that
// name/token on successful validation.
// Returns nil if header is empty or validation fails.
func (b *Basic) Verify(req *http.Request, w http.ResponseWriter, store DataStore, sess SessionStore) (*user_model.User, error) {
	parseBasicRet := b.parseAuthBasic(req)
	authToken, uname := parseBasicRet.authToken, parseBasicRet.uname
	if authToken == "" && uname == "" {
		return nil, nil //nolint:nilnil // the auth method is not applicable
	}
	u, err := b.VerifyAuthToken(req, w, store, sess, authToken)
	if u != nil || err != nil {
		return u, err
	}

	if !setting.Service.EnableBasicAuth {
		return nil, nil //nolint:nilnil // the auth method is not applicable
	}

	// Identity on this instance is Hanzo IAM's alone, so a username and password
	// pair authenticates nobody here. The token path above is the whole of Basic
	// auth — an IAM access token, a repository access token, or an Actions task
	// token — which is what `docker login` and `npm publish` present. Anything
	// else declines rather than falling back to a local credential.
	return nil, nil //nolint:nilnil // the auth method is not applicable
}


func GetAccessScope(store DataStore) auth_model.AccessTokenScope {
	if v, ok := store.GetData()["ApiTokenScope"]; ok {
		return v.(auth_model.AccessTokenScope)
	}
	switch store.GetData()["LoginMethod"] {
	case OAuth2TokenMethodName:
		fallthrough
	case BasicMethodName, AccessTokenMethodName:
		return auth_model.AccessTokenScopeAll
	case ActionTokenMethodName:
		fallthrough
	default:
		return ""
	}
}
