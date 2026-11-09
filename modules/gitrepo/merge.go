// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/git/modules/git/gitcmd"
	"github.com/hanzoai/git/modules/util"
)

// MergeBase checks and returns merge base of two commits.
func MergeBase(ctx context.Context, repo Repository, baseCommitID, headCommitID string) (string, error) {
	mergeBase, stderr, err := RunCmdString(ctx, repo, gitcmd.NewCommand("merge-base").
		AddDashesAndList(baseCommitID, headCommitID))
	if err != nil {
		if gitcmd.IsErrorExitCode(err, 1) && strings.TrimSpace(stderr) == "" {
			return "", util.NewNotExistErrorf("merge-base for %s and %s doesn't exist", baseCommitID, headCommitID)
		}
		return "", fmt.Errorf("get merge-base of %s and %s failed: %w", baseCommitID, headCommitID, err)
	}
	return strings.TrimSpace(mergeBase), nil
}

// IsAncestor reports whether ancestorID is reachable from descendantID, a
// commit counting as its own ancestor. Tags are peeled to the commits they name.
func IsAncestor(ctx context.Context, repo Repository, ancestorID, descendantID string) (bool, error) {
	err := RunCmdWithStderr(ctx, repo, gitcmd.NewCommand("merge-base", "--is-ancestor").
		AddDynamicArguments(ancestorID, descendantID))
	if err == nil {
		return true, nil
	}
	if gitcmd.IsErrorExitCode(err, 1) {
		return false, nil
	}
	return false, fmt.Errorf("is %s an ancestor of %s: %w", ancestorID, descendantID, err)
}
