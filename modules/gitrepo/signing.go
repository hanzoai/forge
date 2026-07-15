// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package gitrepo

import (
	"context"

	"github.com/hanzoai/git/modules/git"
)

func GetSigningKey(ctx context.Context) (*git.SigningKey, *git.Signature) {
	return git.GetSigningKey(ctx)
}
