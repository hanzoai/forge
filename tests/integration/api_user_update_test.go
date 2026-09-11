// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/tests"
)

func TestAPIUpdateUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	normalUsername := "user2"
	session := loginUser(t, normalUsername)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser)

	req := NewRequestWithJSON(t, "PATCH", "/v1/user/settings", map[string]string{
		"website": "https://example.com",
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)
}
