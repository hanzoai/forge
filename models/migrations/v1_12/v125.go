// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_12

import (
	"fmt"

	"github.com/hanzoai/git/models/db"
)

func AddReviewMigrateInfo(x db.EngineMigration) error {
	type Review struct {
		OriginalAuthor   string
		OriginalAuthorID int64
	}

	if err := x.Sync(new(Review)); err != nil {
		return fmt.Errorf("Sync: %w", err)
	}
	return nil
}
