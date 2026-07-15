// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_14

import (
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/modules/timeutil"
)

func AddSessionTable(x db.EngineMigration) error {
	type Session struct {
		Key    string `xorm:"pk CHAR(16)"`
		Data   []byte `xorm:"BLOB"`
		Expiry timeutil.TimeStamp
	}
	return x.Sync(new(Session))
}
