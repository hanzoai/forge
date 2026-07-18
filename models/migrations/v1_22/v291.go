// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_22

import (
	"github.com/hanzoai/git/models/db"

	"github.com/hanzoai/xorm"
)

func AddCommentIDIndexofAttachment(x db.EngineMigration) error {
	type Attachment struct {
		CommentID int64 `xorm:"INDEX"`
	}

	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreDropIndices: true,
		IgnoreConstrains:  true,
	}, &Attachment{})
	return err
}
