// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"strings"

	"github.com/hanzoai/git/modules/log"
)

// IAM configures accepting Hanzo IAM access tokens as git and API credentials.
// Issuer is the switch: while it is empty no IAM token is ever looked at.
var IAM = struct {
	Issuer      string
	LoginSource string
	Audience    string
}{}

func loadIAMFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("iam")
	// A trailing slash would never match the iss claim, so drop it here rather than
	// on every comparison.
	IAM.Issuer = strings.TrimSuffix(strings.TrimSpace(sec.Key("ISSUER").String()), "/")
	IAM.LoginSource = strings.TrimSpace(sec.Key("LOGIN_SOURCE").String())
	IAM.Audience = strings.TrimSpace(sec.Key("AUDIENCE").String())

	if IAM.Issuer == "" {
		return
	}

	// Everything a token is checked against is read from the issuer, so over plain
	// HTTP anyone on the path can hand out their own signing keys and mint whatever
	// they like. Refuse at start-up rather than serve an authentication that only
	// looks like one.
	if !strings.HasPrefix(IAM.Issuer, "https://") {
		log.Fatal("[iam].ISSUER must be an https URL, got %q", IAM.Issuer)
	}

	// The login source carries the account linkage a token is resolved through, so
	// an issuer without one would accept tokens that can never name a user.
	if IAM.LoginSource == "" {
		log.Fatal("[iam].LOGIN_SOURCE must name the OAuth2 login source used with [iam].ISSUER")
	}

	// One issuer serves the whole estate and mints a token per application, each
	// naming its application in aud. Without an audience of our own, a token minted
	// for any other application would be a credential here.
	if IAM.Audience == "" {
		log.Fatal("[iam].AUDIENCE must name the client this forge's tokens are minted for, alongside [iam].ISSUER")
	}
}
