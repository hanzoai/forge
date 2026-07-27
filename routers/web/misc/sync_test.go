// Copyright 2026 Hanzo AI, Inc. All rights reserved.
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

const payload = `{"repository":{"full_name":"o/r"}}`

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidSignature(t *testing.T) {
	good := sign("s3cr3t", payload)

	assert.True(t, validSignature("s3cr3t", good, []byte(payload)))
	assert.False(t, validSignature("other", good, []byte(payload)), "wrong key")
	assert.False(t, validSignature("s3cr3t", good, []byte(payload+" ")), "tampered body")
	assert.False(t, validSignature("s3cr3t", "", []byte(payload)), "absent header")
	assert.False(t, validSignature("s3cr3t", strings.TrimPrefix(good, "sha256="), []byte(payload)), "unprefixed")
	assert.False(t, validSignature("s3cr3t", "sha256=zz", []byte(payload)), "non-hex")
	assert.False(t, validSignature("", good, []byte(payload)), "empty key never validates")
}

func TestRepoFromPayload(t *testing.T) {
	owner, repo, ok := repoFromPayload([]byte(payload))
	assert.True(t, ok)
	assert.Equal(t, "o", owner)
	assert.Equal(t, "r", repo)

	for name, body := range map[string]string{
		"ping":         `{"zen":"Design for failure."}`,
		"no slash":     `{"repository":{"full_name":"lonely"}}`,
		"empty owner":  `{"repository":{"full_name":"/r"}}`,
		"empty repo":   `{"repository":{"full_name":"o/"}}`,
		"not an event": `not json`,
	} {
		_, _, ok := repoFromPayload([]byte(body))
		assert.False(t, ok, name)
	}
}

func post(t *testing.T, body, sigHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body))
	if sigHeader != "" {
		req.Header.Set("X-Hub-Signature-256", sigHeader)
	}
	w := httptest.NewRecorder()
	Sync(w, req)
	return w
}

func TestSyncFailsClosed(t *testing.T) {
	// No secret: refuse everything, including a payload that would verify
	// under some other key.
	defer test.MockVariableValue(&setting.Mirror.SyncSecret, "")()
	assert.Equal(t, http.StatusServiceUnavailable, post(t, payload, sign("k", payload)).Code)
}

func TestSyncRejectsUnsigned(t *testing.T) {
	defer test.MockVariableValue(&setting.Mirror.SyncSecret, "s3cr3t")()

	assert.Equal(t, http.StatusUnauthorized, post(t, payload, "").Code, "missing signature")
	assert.Equal(t, http.StatusUnauthorized, post(t, payload, sign("wrong", payload)).Code, "wrong key")
	assert.Equal(t, http.StatusUnauthorized, post(t, payload+" ", sign("s3cr3t", payload)).Code, "tampered body")
}

func TestSyncPingIsBenign(t *testing.T) {
	// A signed event naming no repository must be a 200 no-op, so the hook
	// shows healthy upstream.
	const ping = `{"zen":"Design for failure."}`
	defer test.MockVariableValue(&setting.Mirror.SyncSecret, "s3cr3t")()

	w := post(t, ping, sign("s3cr3t", ping))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}
