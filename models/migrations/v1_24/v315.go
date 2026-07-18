// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_24

import (
	"github.com/hanzoai/git/models/db"

	"github.com/hanzoai/xorm"
)

func AddEphemeralToActionRunner(x db.EngineMigration) error {
	type ActionRunner struct {
		Ephemeral bool `xorm:"ephemeral NOT NULL DEFAULT false"`
	}
	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreConstrains: true,
		IgnoreIndices:    true,
	}, new(ActionRunner))
	return err
}
