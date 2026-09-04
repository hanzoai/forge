// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import "github.com/hanzoai/git/modules/log"

// IAM is the identity provider this instance verifies credentials against.
//
// The deployment already writes this block — `[iam] ISSUER / AUDIENCE /
// LOGIN_SOURCE` is in the shipped app.ini — and until now nothing in Go read it.
// It was inert configuration describing the one thing the instance most needed to
// know, which is why the verifier had no source but the database.
//
// It exists to DECOUPLE two questions that are not the same:
//
//   - "can I verify a bearer this issuer signed" — needs the issuer's public keys
//     and nothing else, so it needs no secret and no row;
//   - "can a browser sign in here" — needs a registered OAuth2 login source,
//     because the authorization-code exchange authenticates the client.
//
// Coupling them cost the second's prerequisites to the first: with no login
// source configured, a git push, a CI clone and a package pull — every one of
// which presents a bearer and needs no client credential at all — could not be
// verified either.
var IAM = struct {
	// Issuer is the OIDC issuer, e.g. https://hanzo.id. Discovery is derived from
	// it, so a deployment states one URL rather than two that can disagree.
	Issuer string `ini:"ISSUER"`
	// Audience is this instance's own client id, e.g. hanzo-git. Recorded for
	// operators and for a future audience check; the issuer allowlist is what
	// fails closed today (see services/auth/iam.go).
	Audience string `ini:"AUDIENCE"`
}{}

func loadIAMFrom(rootCfg ConfigProvider) {
	if err := rootCfg.Section("iam").MapTo(&IAM); err != nil {
		log.Fatal("Failed to map IAM settings: %v", err)
	}
}
