// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"context"

	"github.com/hanzoai/git/modules/git/gitcmd"
)

func UpdateRef(ctx context.Context, repo Repository, refName, newCommitID string) error {
	return RunCmd(ctx, repo, gitcmd.NewCommand("update-ref").AddDynamicArguments(refName, newCommitID))
}

// SwapRef points refName at newCommitID only while it still holds oldCommitID,
// so a concurrent writer makes the swap fail instead of being overwritten.
// The zero ID as oldCommitID demands that refName does not exist yet.
func SwapRef(ctx context.Context, repo Repository, refName, newCommitID, oldCommitID string) error {
	return RunCmd(ctx, repo, gitcmd.NewCommand("update-ref").AddDynamicArguments(refName, newCommitID, oldCommitID))
}

func RemoveRef(ctx context.Context, repo Repository, refName string) error {
	return RunCmd(ctx, repo, gitcmd.NewCommand("update-ref", "--no-deref", "-d").
		AddDynamicArguments(refName))
}
