// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_16

import "github.com/hanzoai/git/models/db"

func IncreaseCredentialIDTo410(x db.EngineMigration) error {
	// no-op
	// v208 was completely wrong
	// So now we have to no-op again.

	return nil
}
