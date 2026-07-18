// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"github.com/hanzoai/git/models/db"

	"github.com/hanzoai/xorm"
)

type mirrorWithLastSyncUnix struct {
	LastSyncUnix int64 `xorm:"INDEX"`
}

func (mirrorWithLastSyncUnix) TableName() string {
	return "mirror"
}

func AddLastSyncUnixToMirror(x db.EngineMigration) error {
	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreDropIndices: true,
	}, new(mirrorWithLastSyncUnix))
	return err
}
