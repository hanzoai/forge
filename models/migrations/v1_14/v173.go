// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_14

import (
	"fmt"

	"github.com/hanzoai/git/models/db"
)

func AddTimeIDCommentColumn(x db.EngineMigration) error {
	type Comment struct {
		TimeID int64
	}

	if err := x.Sync(new(Comment)); err != nil {
		return fmt.Errorf("Sync: %w", err)
	}
	return nil
}
