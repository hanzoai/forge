// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strings"
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

// org3/repo5 is a pull mirror. A caller without write access gets a 403 and queues nothing.
func TestMirrorSyncQueuesOnlyForWriter(t *testing.T) {
	unittest.PrepareTestEnv(t)
	defer test.MockVariableValue(&setting.Mirror.Enabled, true)()
	var queued []int64
	defer test.MockVariableValue(&queuePull, func(repoID int64) { queued = append(queued, repoID) })()

	// An anonymous caller can read this public repo, not write it.
	ctx, resp := contexttest.MockAPIContext(t, "org3/repo5")
	contexttest.LoadRepo(t, ctx, 5)
	MirrorSync(ctx)
	assert.Equal(t, http.StatusForbidden, ctx.Resp.WrittenStatus())
	assert.Equal(t, 1, strings.Count(resp.Body.String(), "Must have write access"), "one answer, not a 403 and then another")
	assert.Empty(t, queued)

	// An owner of the organization can.
	ctx, _ = contexttest.MockAPIContext(t, "org3/repo5")
	contexttest.LoadUser(t, ctx, 2)
	contexttest.LoadRepo(t, ctx, 5)
	MirrorSync(ctx)
	assert.Equal(t, http.StatusOK, ctx.Resp.WrittenStatus())
	assert.Equal(t, []int64{5}, queued)
}
