// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
)

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidMirrorSignature(t *testing.T) {
	const secret, body = "s3cr3t", `{"repository":{"full_name":"o/r"}}`
	good := sign(secret, body)

	assert.True(t, validMirrorSignature(secret, good, []byte(body)))
	assert.False(t, validMirrorSignature("other", good, []byte(body)), "wrong key")
	assert.False(t, validMirrorSignature(secret, good, []byte(body+" ")), "tampered body")
	assert.False(t, validMirrorSignature(secret, "", []byte(body)), "absent header")
	assert.False(t, validMirrorSignature(secret, strings.TrimPrefix(good, "sha256="), []byte(body)), "unprefixed")
	assert.False(t, validMirrorSignature(secret, "sha256=zz", []byte(body)), "non-hex")
	assert.False(t, validMirrorSignature("", good, []byte(body)), "empty key never validates")
}

func post(t *testing.T, body, sigHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/mirror/github", strings.NewReader(body))
	if sigHeader != "" {
		req.Header.Set("X-Hub-Signature-256", sigHeader)
	}
	w := httptest.NewRecorder()
	MirrorGitHubWebhook(w, req)
	return w
}

func TestMirrorGitHubWebhookFailsClosed(t *testing.T) {
	const body = `{"repository":{"full_name":"o/r"}}`

	// No secret configured: the endpoint refuses everything, even a payload
	// that would otherwise verify under some other key.
	defer test.MockVariableValue(&setting.Mirror.GithubWebhookSecret, "")()
	assert.Equal(t, http.StatusServiceUnavailable, post(t, body, sign("k", body)).Code)
}

func TestMirrorGitHubWebhookRejectsUnsigned(t *testing.T) {
	const secret, body = "s3cr3t", `{"repository":{"full_name":"o/r"}}`
	defer test.MockVariableValue(&setting.Mirror.GithubWebhookSecret, secret)()

	assert.Equal(t, http.StatusUnauthorized, post(t, body, "").Code, "missing signature")
	assert.Equal(t, http.StatusUnauthorized, post(t, body, sign("wrong", body)).Code, "wrong key")
	assert.Equal(t, http.StatusUnauthorized, post(t, body+" ", sign(secret, body)).Code, "tampered body")
}

func TestMirrorGitHubWebhookPingIsBenign(t *testing.T) {
	// A signed event that names no repository (GitHub's ping on hook
	// creation) must be a 200 no-op, so the hook shows healthy upstream.
	const secret, body = "s3cr3t", `{"zen":"Design for failure."}`
	defer test.MockVariableValue(&setting.Mirror.GithubWebhookSecret, secret)()

	w := post(t, body, sign(secret, body))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}
