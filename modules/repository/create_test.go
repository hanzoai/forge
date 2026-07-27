// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"

	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/gitrepo"

	"github.com/stretchr/testify/assert"
)

func TestGetDirectorySize(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	repo, err := repo_model.GetRepositoryByID(t.Context(), 1)
	assert.NoError(t, err)
	size, err := gitrepo.CalcRepositorySize(repo)
	assert.NoError(t, err)

	// Measured, not memorised. This was `repo.Size = 8165 // real size on the
	// disk`, and the fixture repository is not frozen — any commit that changes
	// a byte in it makes the number a lie. It had drifted to 8141.
	//
	// Walking the same tree here keeps what the assertion is actually for: that
	// CalcRepositorySize sums every regular file under the repo and skips
	// directories, rather than returning zero, a file count, or one directory's
	// worth.
	var want int64
	require.NoError(t, filepath.WalkDir(repo.RepoPath(), func(_ string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil // transient temp/lock files, same as the function under test
		} else if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		want += info.Size()
		return nil
	}))

	assert.Positive(t, want, "fixture repo should not be empty")
	assert.Equal(t, want, size)
}
