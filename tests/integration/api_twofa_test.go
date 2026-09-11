// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/tests"

	"github.com/stretchr/testify/assert"
)

func TestBasicAuthWithWebAuthn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// user1 has no webauthn enrolled, he can request API with basic auth
	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	unittest.AssertNotExistsBean(t, &auth_model.WebAuthnCredential{UserID: user1.ID})
	req := NewRequest(t, "GET", "/v1/user")
	req.SetBasicAuth(user1.Name, "password")
	MakeRequest(t, req, http.StatusOK)

	// user1 has no webauthn enrolled, he can request git protocol with basic auth
	req = NewRequest(t, "GET", "/user2/repo1/info/refs")
	req.SetBasicAuth(user1.Name, "password")
	MakeRequest(t, req, http.StatusOK)

	// user1 has no webauthn enrolled, he can request container package with basic auth
	req = NewRequest(t, "GET", "/v2/token")
	req.SetBasicAuth(user1.Name, "password")
	resp := MakeRequest(t, req, http.StatusOK)

	type tokenResponse struct {
		Token string `json:"token"`
	}
	tokenParsed := DecodeJSON(t, resp, &tokenResponse{})
	assert.NotEmpty(t, tokenParsed.Token)

	// user32 has webauthn enrolled, he can't request API with basic auth
	user32 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 32})
	unittest.AssertExistsAndLoadBean(t, &auth_model.WebAuthnCredential{UserID: user32.ID})

	req = NewRequest(t, "GET", "/v1/user")
	req.SetBasicAuth(user32.Name, "notpassword")
	resp = MakeRequest(t, req, http.StatusUnauthorized)

	type userResponse struct {
		Message string `json:"message"`
	}
	userParsed := DecodeJSON(t, resp, &userResponse{})
	assert.Equal(t, "basic authorization is not allowed while WebAuthn enrolled", userParsed.Message)

	// user32 has webauthn enrolled, he can't request git protocol with basic auth
	req = NewRequest(t, "GET", "/user2/repo1/info/refs")
	req.SetBasicAuth(user32.Name, "notpassword")
	MakeRequest(t, req, http.StatusUnauthorized)

	// user32 has webauthn enrolled, he can't request container package with basic auth
	req = NewRequest(t, "GET", "/v2/token")
	req.SetBasicAuth(user1.Name, "notpassword")
	MakeRequest(t, req, http.StatusUnauthorized)
}
