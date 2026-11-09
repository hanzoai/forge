// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/git/models/unittest"
	api "github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/modules/web"
	"github.com/hanzoai/git/services/contexttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A name that git may read as an object id is refused as one that cannot be used:
// 422, with a message, not a 500.
func TestObjectNamesAreUnprocessableOnTheAPI(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	name := strings.Repeat("ab", 20)

	t.Run("rename a branch", func(t *testing.T) {
		ctx, resp := contexttest.MockAPIContext(t, "user2/repo1")
		contexttest.LoadUser(t, ctx, 2)
		contexttest.LoadRepo(t, ctx, 1)
		ctx.SetPathParam("*", "branch2")
		web.SetForm(ctx, &api.RenameBranchRepoOption{Name: name})

		RenameBranch(ctx)

		assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)
		assert.Contains(t, resp.Body.String(), "object id")
	})

	t.Run("create a tag", func(t *testing.T) {
		ctx, resp := contexttest.MockAPIContext(t, "user2/repo1")
		contexttest.LoadUser(t, ctx, 2)
		contexttest.LoadRepo(t, ctx, 1)
		contexttest.LoadGitRepo(t, ctx)
		web.SetForm(ctx, &api.CreateTagOption{TagName: name, Target: "master"})

		CreateTag(ctx)

		assert.Equal(t, http.StatusUnprocessableEntity, resp.Code)
		assert.Contains(t, resp.Body.String(), "invalid tag name")
	})
}
