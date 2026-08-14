// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"github.com/hanzoai/git/models/db"

	"github.com/hanzoai/xorm"
)

// AddDropsToActionRunJob adds the Drops column to ActionRunJob, counting the
// times a runner took the job and gave it back having run none of its steps.
func AddDropsToActionRunJob(x db.EngineMigration) error {
	type ActionRunJob struct {
		Drops int64 `xorm:"NOT NULL DEFAULT 0"`
	}

	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreDropIndices: true,
		IgnoreConstrains:  true,
	}, new(ActionRunJob))
	return err
}
