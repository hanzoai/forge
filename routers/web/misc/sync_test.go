// Copyright 2026 Hanzo AI, Inc. All rights reserved.
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

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestIsGitHubRepo(t *testing.T) {
	for address, want := range map[string]bool{
		"https://github.com/hanzo-inc/app":                true,
		"http://github.com/hanzo-inc/app":                 true,
		"https://github.com/hanzo-inc/app.git":            true,
		"https://github.com/hanzo-inc/app/":               true,
		"https://github.com/hanzo-inc/app.git/":           true,
		"https://user:token@github.com/hanzo-inc/app.git": true,
		"https://x-access-token@github.com/hanzo-inc/app": true,
		"HTTPS://GitHub.com/Hanzo-Inc/APP":                true,
		"https://github.com:443/hanzo-inc/app":            true,
		"ssh://git@github.com/hanzo-inc/app.git":          true,
		"git@github.com:hanzo-inc/app.git":                true,
		"https://github.com/hanzo-inc/other":              false,
		"https://github.com/other/app":                    false,
		"https://github.com/hanzo-inc/app-extra":          false,
		"https://github.com/hanzo-inc/app/tree/main":      false,
		"https://github.com/app":                          false,
		"https://gitlab.com/hanzo-inc/app":                false,
		"https://notgithub.com/hanzo-inc/app":             false,
		"https://github.com.evil.example/hanzo-inc/app":   false,
		"https://evil.example/github.com/hanzo-inc/app":   false,
		"/srv/git/github.com/hanzo-inc/app":               false,
		"":                                                false,
	} {
		assert.Equal(t, want, isGitHubRepo(address, "hanzo-inc", "app"), address)
	}
}

// queued records what Sync queues, and answers for the tests that call it.
func queued(t *testing.T) (pulls, inbound *[]int64) {
	t.Helper()
	pulls, inbound = new([]int64), new([]int64)
	t.Cleanup(test.MockVariableValue(&queuePull, func(id int64) { *pulls = append(*pulls, id) }))
	t.Cleanup(test.MockVariableValue(&queueInbound, func(id int64) { *inbound = append(*inbound, id) }))
	t.Cleanup(test.MockVariableValue(&setting.Mirror.SyncSecret, "s3cr3t"))
	return pulls, inbound
}

func event(t *testing.T, fullName string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"repository":{"full_name":"` + fullName + `"}}`
	return post(t, body, sign("s3cr3t", body))
}

func TestSyncQueuesInboundForRegularRepo(t *testing.T) {
	unittest.PrepareTestEnv(t)
	pulls, inbound := queued(t)

	// user2/repo1 is a regular repo; the second mirror is the one that points at the event's repository.
	other := &repo_model.PushMirror{RepoID: 1, RemoteName: "elsewhere", RemoteAddress: "https://github.com/user2/unrelated"}
	match := &repo_model.PushMirror{RepoID: 1, RemoteName: "github", RemoteAddress: "https://token@github.com/User2/Repo1.git"}
	require.NoError(t, db.Insert(t.Context(), other))
	require.NoError(t, db.Insert(t.Context(), match))

	w := event(t, "user2/repo1")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "queued", w.Body.String())
	assert.Equal(t, []int64{match.ID}, *inbound)
	assert.Empty(t, *pulls)
}

func TestSyncIgnoresRegularRepoWithoutMatchingPushMirror(t *testing.T) {
	unittest.PrepareTestEnv(t)
	pulls, inbound := queued(t)

	w := event(t, "user2/repo1")
	assert.Equal(t, "ok", w.Body.String(), "no push mirror")

	// A push mirror to somewhere else, or to another repository, is not the way back in.
	for _, address := range []string{"https://gitlab.com/user2/repo1", "https://github.com/user2/repo2", "https://github.com/org3/repo1"} {
		require.NoError(t, db.Insert(t.Context(), &repo_model.PushMirror{RepoID: 1, RemoteName: "r", RemoteAddress: address}))
	}
	w = event(t, "user2/repo1")
	assert.Equal(t, "ok", w.Body.String(), "push mirrors to other repositories")

	w = event(t, "user2/nonesuch")
	assert.Equal(t, "ok", w.Body.String(), "unknown repository")

	assert.Empty(t, *inbound)
	assert.Empty(t, *pulls)
}

func TestSyncQueuesPullForMirrorRepo(t *testing.T) {
	unittest.PrepareTestEnv(t)
	pulls, inbound := queued(t)

	// org3/repo5 has a pull mirror row; a push mirror beside it must not turn the pull into an inbound sync.
	require.NoError(t, db.Insert(t.Context(), &repo_model.PushMirror{RepoID: 5, RemoteName: "github", RemoteAddress: "https://github.com/org3/repo5"}))

	w := event(t, "org3/repo5")

	assert.Equal(t, "queued", w.Body.String())
	assert.Equal(t, []int64{5}, *pulls)
	assert.Empty(t, *inbound)
}
