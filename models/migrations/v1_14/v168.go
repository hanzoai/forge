// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_14

import "github.com/hanzoai/git/models/db"

func RecreateUserTableToFixDefaultValues(_ db.EngineMigration) error {
	return nil
}
