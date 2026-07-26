// Copyright 2026 The Hanzo Git Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gitea->gitd binary rename moved the delegate file every repository's
// "<hook>" script executes. The outer script runs EVERY executable in
// "<hook>.d/" and rejects the push on any non-zero exit, so a delegate left over
// from the old name would invoke a binary path that no longer exists and break
// every push. Regeneration must delete it, and the checker must see it.
func TestCreateDelegateHooksRemovesLegacyDelegate(t *testing.T) {
	hookDir := filepath.Join(t.TempDir(), "hooks")
	require.NoError(t, os.MkdirAll(filepath.Join(hookDir, "pre-receive.d"), os.ModePerm))

	legacy := filepath.Join(hookDir, "pre-receive.d", "gitea")
	require.NoError(t, os.WriteFile(legacy, []byte("#!/usr/bin/env bash\n/usr/local/bin/gitea hook pre-receive\n"), 0o777))

	results, err := checkDelegateHooks(hookDir)
	require.NoError(t, err)
	assert.Contains(t, results, "legacy hook file "+legacy+" still exists and would reject every push")

	require.NoError(t, createDelegateHooks(hookDir))

	assert.NoFileExists(t, legacy)
	assert.FileExists(t, filepath.Join(hookDir, "pre-receive.d", "gitd"))

	// A repository that has been regenerated is clean: no leftovers, nothing
	// out of date. This is what the live server reaches on its first boot after
	// the rename, via routers.syncAppConfForGit -> SyncRepositoryHooks.
	results, err = checkDelegateHooks(hookDir)
	require.NoError(t, err)
	assert.Empty(t, results)
}
