// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mirror sync keeps its record in refs/upstream, which no listing shows.
func TestGetRefsFilteredLeavesOutTheUpstreamNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.git")
	require.NoError(t, InitRepository(t.Context(), path, true, Sha1ObjectFormat.Name()))
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = path
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	tree := run("hash-object", "-t", "tree", "-w", "--stdin")
	commit := run("commit-tree", tree, "-m", "one")
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1", "refs/upstream/heads/main", "refs/upstream/staging/tags/v1", "refs/upstream/refused/x"} {
		run("update-ref", ref, commit)
	}

	repo, err := OpenRepository(t.Context(), path)
	require.NoError(t, err)
	defer repo.Close()

	var names []string
	refs, err := repo.GetRefsFiltered("")
	require.NoError(t, err)
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	assert.ElementsMatch(t, []string{"refs/heads/main", "refs/tags/v1"}, names)

	refs, err = repo.GetRefsFiltered("refs/upstream")
	require.NoError(t, err)
	assert.Empty(t, refs)
}
