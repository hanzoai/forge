// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testReposDir = "tests/repos/"

func TestVerifyCommits(t *testing.T) {
	unittest.PrepareTestEnv(t)

	gitRepo, err := git.OpenRepository(t.Context(), testReposDir+"repo1_hook_verification")
	if err != nil {
		defer gitRepo.Close()
	}
	assert.NoError(t, err)

	objectFormat, err := gitRepo.GetObjectFormat()
	assert.NoError(t, err)

	testCases := []struct {
		base, head string
		verified   bool
	}{
		{"72920278f2f999e3005801e5d5b8ab8139d3641c", "d766f2917716d45be24bfa968b8409544941be32", true},
		{objectFormat.EmptyObjectID().String(), "93eac826f6188f34646cea81bf426aa5ba7d3bfe", true}, // New branch with verified commit
		{"9779d17a04f1e2640583d35703c62460b2d86e0a", "72920278f2f999e3005801e5d5b8ab8139d3641c", false},
		{objectFormat.EmptyObjectID().String(), "9ce3f779ae33f31fce17fac3c512047b75d7498b", false}, // New branch with unverified commit
	}

	for _, tc := range testCases {
		err = verifyCommits(tc.base, tc.head, gitRepo, nil)
		if tc.verified {
			assert.NoError(t, err)
		} else {
			assert.Error(t, err)
		}
	}
}

// The mirror sync fetches a branch into refs/upstream before it pushes it, so the
// commits are already reachable from a ref when pre-receive looks at the new
// branch. A branch created that way must have its commits checked all the same.
func TestVerifyCommitsOfABranchTheSyncCreates(t *testing.T) {
	unittest.PrepareTestEnv(t)

	const unsigned = "9ce3f779ae33f31fce17fac3c512047b75d7498b"
	path := filepath.Join(t.TempDir(), "repo.git")
	for _, args := range [][]string{
		{"clone", "-q", "--bare", testReposDir + "repo1_hook_verification", path},
		{"-C", path, "update-ref", "refs/upstream/staging/heads/sigtest/a", unsigned},
		{"-C", path, "update-ref", "refs/upstream/heads/sigtest/a", unsigned},
	} {
		out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	gitRepo, err := git.OpenRepository(t.Context(), path)
	require.NoError(t, err)
	defer gitRepo.Close()
	objectFormat, err := gitRepo.GetObjectFormat()
	require.NoError(t, err)

	err = verifyCommits(objectFormat.EmptyObjectID().String(), unsigned, gitRepo, nil)
	require.Error(t, err)
	assert.True(t, isErrUnverifiedCommit(err))

	// Any other ref that reaches the commits is a reason to take them as already there.
	out, err := exec.CommandContext(t.Context(), "git", "-C", path, "update-ref", "refs/heads/elsewhere", unsigned).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.NoError(t, verifyCommits(objectFormat.EmptyObjectID().String(), unsigned, gitRepo, nil))
}
