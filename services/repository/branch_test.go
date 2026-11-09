// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/gitrepo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refspec whose source is a bare commit id is read as a ref's name before an
// object's, and a writer may have made refs/tags/<id> or refs/heads/<id> to be
// read in its place. A branch made from a commit lands on the commit.
func TestBranchPushesIgnoreRefsNamedLikeTheCommit(t *testing.T) {
	unittest.PrepareTestEnv(t)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	path := repo_model.RepoPath(repo.OwnerName, repo.Name)
	// The fixture's hooks call a daemon that is not here.
	require.NoError(t, os.RemoveAll(filepath.Join(path, "hooks")))
	git := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", path}, args...)...)
		out, err := cmd.Output()
		require.NoError(t, err, args)
		return strings.TrimSpace(string(out))
	}

	c, err := gitrepo.GetBranchCommitID(t.Context(), repo, "master")
	require.NoError(t, err)
	var e string // any other commit
	for id := range strings.FieldsSeq(git("rev-list", "--all")) {
		if id != c {
			e = id
			break
		}
	}
	require.NotEmpty(t, e)

	for i, decoy := range []string{"refs/tags/" + c, "refs/heads/" + c, "refs/" + c} {
		git("update-ref", decoy, e)
		branch := "from-the-commit-" + string(rune('a'+i))

		require.NoError(t, CreateNewBranchFromCommit(t.Context(), user, repo, c, branch), decoy)
		assert.Equal(t, c, git("rev-parse", "--verify", "refs/heads/"+branch), "a decoy %s took the commit's place", decoy)

		git("update-ref", "-d", decoy)
	}

	// And a branch moved to a commit lands on the commit.
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	git("update-ref", "refs/tags/"+c, e)
	require.NoError(t, UpdateBranch(t.Context(), repo, gitRepo, user, "branch2", c, "", true))
	assert.Equal(t, c, git("rev-parse", "--verify", "refs/heads/branch2"), "a decoy took the commit's place")
}
