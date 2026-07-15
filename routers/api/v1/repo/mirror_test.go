// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"testing"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/services/contexttest"

	"github.com/stretchr/testify/assert"
)

// TestPushMirrorSync verifies the endpoint attempts every push mirror instead
// of aborting on the first failure, reporting all failed remotes with a 422.
// Each remote name is not a configured git remote, so SyncPushMirror fails fast
// without any network access.
func TestPushMirrorSync(t *testing.T) {
	unittest.PrepareTestEnv(t)
	defer test.MockVariableValue(&setting.Mirror.Enabled, true)()

	for _, remoteName := range []string{"broken_remote_1", "broken_remote_2"} {
		assert.NoError(t, db.Insert(t.Context(), &repo_model.PushMirror{RepoID: 1, RemoteName: remoteName}))
	}

	ctx, resp := contexttest.MockAPIContext(t, "user2/repo1")
	contexttest.LoadRepo(t, ctx, 1)

	PushMirrorSync(ctx)

	assert.Equal(t, http.StatusUnprocessableEntity, ctx.Resp.WrittenStatus())
	assert.Contains(t, resp.Body.String(), "broken_remote_1")
	assert.Contains(t, resp.Body.String(), "broken_remote_2")
}
