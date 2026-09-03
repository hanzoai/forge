// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// Package auth ends a session. It no longer starts one.
//
// This instance authenticates against Hanzo IAM and nothing else, so it holds no
// credential to check and issues none. What used to live here was a second
// identity provider running beside hanzo.id — measured on the live instance, it
// served its own /.well-known/openid-configuration (200), its own JWKS at
// /login/oauth/keys (200) and its own /login/oauth/authorize (303) — plus local
// password sign-in and sign-up, e-mail activation, password reset, TOTP,
// WebAuthn, OpenID Connect consumption, and account linking between all of them.
//
// A credential is verified in exactly one place now: services/auth/iam.go, which
// checks that Hanzo IAM signed the bearer against the issuer's published keys.
// That path needs no client secret and no registered login source, so every
// credential-presenting caller — a git push, a CI clone, a package pull — works
// with nothing configured here.
//
// WHAT THIS COSTS, STATED PLAINLY: there is no browser sign-in. The redirect to
// hanzo.id and back is not built, and it cannot be built out of what was removed
// — that machinery WAS the second provider. A human reaches this instance with a
// token until it exists.
package auth

import (
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/services/context"
)

// SignOut ends the session this instance is holding.
//
// Ending a session is not authentication: it destroys state we already have and
// asks nobody anything, which is why it survives a package that otherwise issues
// nothing. It does not reach IAM — signing out there is IAM's own surface, and
// driving it from here would make one action two systems' business.
func SignOut(ctx *context.Context) {
	HandleSignOut(ctx)
	ctx.Redirect(setting.AppSubURL + "/")
}

// HandleSignOut drops the session without answering the request, for a caller
// that has its own answer to write — the event stream ends a revoked session
// mid-poll and must close the stream rather than redirect it.
func HandleSignOut(ctx *context.Context) {
	ctx.Session.Destroy(ctx.Resp, ctx.Req)
	ctx.DeleteSiteCookie(setting.SessionConfig.CookieName)
}
