// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"path/filepath"
	"testing"

	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/setting"
)

func TestMain(m *testing.M) {
	// resolve repository path relative to the test directory
	setting.SetupGitTestEnv()
	gitRoot := setting.GetGitTestSourceRoot()
	repoPath = func(repo Repository) string {
		if filepath.IsAbs(repo.RelativePath()) {
			return repo.RelativePath() // for testing purpose only
		}
		return filepath.Join(gitRoot, "modules/git/tests/repos", repo.RelativePath())
	}
	git.RunGitTests(m)
}
