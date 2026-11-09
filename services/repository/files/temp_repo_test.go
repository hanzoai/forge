// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package files

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemporaryUploadRepository(t *testing.T) {
	mockedRepo := &repo_model.Repository{Name: "mocked-repo-name", OwnerName: "mocked-owner-name"}

	doTest := func(t *testing.T, objectFormatName string) {
		tmpGitRepo, err := NewTemporaryUploadRepository(mockedRepo)
		require.NoError(t, err)
		defer tmpGitRepo.Close()

		require.NoError(t, tmpGitRepo.Init(t.Context(), objectFormatName))

		require.NoError(t, tmpGitRepo.RemoveFilesFromIndex(t.Context(), "any-file-name"))
		require.NoError(t, tmpGitRepo.RemoveFilesFromIndex(t.Context(), "--any-file-name"))

		objID, err := tmpGitRepo.HashObjectAndWrite(t.Context(), bytes.NewReader(nil))
		require.NoError(t, err)
		require.NoError(t, tmpGitRepo.AddObjectToIndex(t.Context(), "100644", objID, "any-file-name"))
		require.NoError(t, tmpGitRepo.AddObjectToIndex(t.Context(), "100644", objID, "--any-file-name"))
	}

	t.Run("sha1", func(t *testing.T) {
		doTest(t, git.Sha1ObjectFormat.Name())
	})

	t.Run("sha256", func(t *testing.T) {
		if !git.DefaultFeatures().SupportHashSha256 {
			t.Skip("sha256 is not supported")
		}
		doTest(t, git.Sha256ObjectFormat.Name())
	})
}

// The commit to push is named to git as an id, which it reads as the name of a ref
// before it reads it as an object. A ref of that name does not take its place.
func TestTemporaryUploadRepositoryPushesTheCommit(t *testing.T) {
	unittest.PrepareTestEnv(t)
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	// The fixture's hooks call a daemon that is not here.
	require.NoError(t, os.RemoveAll(filepath.Join(repo_model.RepoPath(repo.OwnerName, repo.Name), "hooks")))

	tmp, err := NewTemporaryUploadRepository(repo)
	require.NoError(t, err)
	defer tmp.Close()
	require.NoError(t, tmp.Clone(t.Context(), "master", false))

	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = tmp.basePath
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.Output()
		require.NoError(t, err, args)
		return strings.TrimSpace(string(out))
	}
	head := run("rev-parse", "HEAD")
	commit := run("commit-tree", head+"^{tree}", "-p", head, "-m", "the commit to push")
	run("update-ref", "refs/tags/"+commit, head) // a ref of that name, elsewhere

	require.NoError(t, tmp.Push(t.Context(), doer, commit, "pushed", false))

	pushed := exec.CommandContext(t.Context(), "git", "-C", repo_model.RepoPath(repo.OwnerName, repo.Name), "rev-parse", "--verify", "refs/heads/pushed")
	out, err := pushed.Output()
	require.NoError(t, err)
	assert.Equal(t, commit, strings.TrimSpace(string(out)))
}
