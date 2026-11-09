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
	"github.com/hanzoai/git/modules/git"
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
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", path}, args...)...)
		out, err := cmd.Output()
		require.NoError(t, err, args)
		return strings.TrimSpace(string(out))
	}

	c, err := gitrepo.GetBranchCommitID(t.Context(), repo, "master")
	require.NoError(t, err)
	var e string // any other commit
	for id := range strings.FieldsSeq(run("rev-list", "--all")) {
		if id != c {
			e = id
			break
		}
	}
	require.NotEmpty(t, e)

	for i, decoy := range []string{"refs/tags/" + c, "refs/heads/" + c, "refs/" + c} {
		run("update-ref", decoy, e)
		branch := "from-the-commit-" + string(rune('a'+i))

		require.NoError(t, CreateNewBranchFromCommit(t.Context(), user, repo, c, branch), decoy)
		assert.Equal(t, c, run("rev-parse", "--verify", "refs/heads/"+branch), "a decoy %s took the commit's place", decoy)

		run("update-ref", "-d", decoy)
	}

	// And a branch moved to a commit lands on the commit.
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	run("update-ref", "refs/tags/"+c, e)
	require.NoError(t, UpdateBranch(t.Context(), repo, gitRepo, user, "branch2", c, "", true))
	assert.Equal(t, c, run("rev-parse", "--verify", "refs/heads/branch2"), "a decoy took the commit's place")
}

// No hook sees a rename, and a branch named like an object id can be read as the
// object, so neither a rename nor a new branch may take such a name.
func TestBranchRefusesANameLikeAnObjectId(t *testing.T) {
	unittest.PrepareTestEnv(t)
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	c, err := gitrepo.GetBranchCommitID(t.Context(), repo, "master")
	require.NoError(t, err)

	for _, name := range []string{c, strings.ToUpper(c), strings.Repeat("ab", 32)} {
		_, err := RenameBranch(t.Context(), repo, user, "branch2", name)
		assert.ErrorIs(t, err, ErrBranchNameLikeObject, "rename to %s", name)
		assert.ErrorIs(t, CreateNewBranchFromCommit(t.Context(), user, repo, c, name), ErrBranchNameLikeObject, "create %s", name)
		assert.ErrorIs(t, CreateNewBranch(t.Context(), user, repo, "master", name), ErrBranchNameLikeObject, "create %s from a branch", name)
		assert.False(t, gitrepo.IsReferenceExist(t.Context(), repo, git.BranchPrefix+name))
	}
}
