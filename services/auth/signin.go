// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"

	"github.com/hanzoai/git/models/auth"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/util"

	_ "github.com/hanzoai/git/services/auth/source/oauth2" // registers the OIDC source Hanzo IAM is configured as
)

// ErrPasswordAuth is returned for every username-and-password sign-in attempt.
//
// Identity on this instance is Hanzo IAM's, reached over OIDC and presented as a
// bearer this instance verifies rather than issues (see iam.go). There is no local
// credential to check a password against: the sources that held one — db, ldap,
// pam, smtp, sspi — are gone rather than disabled, so this refuses by construction
// instead of by configuration.
var ErrPasswordAuth = util.NewInvalidArgumentErrorf("password authentication is not available; sign in with Hanzo IAM")

// UserSignIn refuses. It survives only so the callers that still reference a
// password form fail closed while their routes and templates are removed; it
// authenticates nobody and has no path that can.
func UserSignIn(_ context.Context, _, _ string) (*user_model.User, *auth.Source, error) {
	return nil, nil, ErrPasswordAuth
}
