// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_19

import "github.com/hanzoai/git/models/db"

func DropForeignReferenceTable(x db.EngineMigration) error {
	// Drop the table introduced in `v211`, it's considered badly designed and doesn't look like to be used.
	type ForeignReference struct{}
	return x.DropTables(new(ForeignReference))
}
