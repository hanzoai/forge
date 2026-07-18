// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_23

import (
	"github.com/hanzoai/git/models/db"

	"github.com/hanzoai/xorm"
)

func AddBlockAdminMergeOverrideBranchProtection(x db.EngineMigration) error {
	type ProtectedBranch struct {
		BlockAdminMergeOverride bool `xorm:"NOT NULL DEFAULT false"`
	}
	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreConstrains: true,
		IgnoreIndices:    true,
	}, new(ProtectedBranch))
	return err
}
