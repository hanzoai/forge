// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"context"

	"github.com/hanzoai/git/modules/git/gitcmd"
	"github.com/hanzoai/git/modules/globallock"
)

func getRepoConfigLockKey(repoStoragePath string) string {
	return "repo-config:" + repoStoragePath
}

// GitConfigAdd add a git configuration key to a specific value for the given repository.
func GitConfigAdd(ctx context.Context, repo Repository, key, value string) error {
	return globallock.LockAndDo(ctx, getRepoConfigLockKey(repo.RelativePath()), func(ctx context.Context) error {
		_, _, err := RunCmdString(ctx, repo, gitcmd.NewCommand("config", "--add").
			AddDynamicArguments(key, value))
		return err
	})
}

// GitConfigSet updates a git configuration key to a specific value for the given repository.
// If the key does not exist, it will be created.
// If the key exists, it will be updated to the new value.
func GitConfigSet(ctx context.Context, repo Repository, key, value string) error {
	return globallock.LockAndDo(ctx, getRepoConfigLockKey(repo.RelativePath()), func(ctx context.Context) error {
		_, _, err := RunCmdString(ctx, repo, gitcmd.NewCommand("config").
			AddDynamicArguments(key, value))
		return err
	})
}

// GitConfigSetAll makes values, in that order, the whole of the multi-valued key.
func GitConfigSetAll(ctx context.Context, repo Repository, key string, values ...string) error {
	return globallock.LockAndDo(ctx, getRepoConfigLockKey(repo.RelativePath()), func(ctx context.Context) error {
		// The first value replaces every value the key has, in one write; the rest follow it.
		for i, value := range values {
			cmd := gitcmd.NewCommand("config", "--add")
			if i == 0 {
				cmd = gitcmd.NewCommand("config", "--replace-all")
			}
			if _, _, err := RunCmdString(ctx, repo, cmd.AddDynamicArguments(key, value)); err != nil {
				return err
			}
		}
		return nil
	})
}
