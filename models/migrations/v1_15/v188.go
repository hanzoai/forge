// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_15

import "github.com/hanzoai/git/models/db"

func AddKeyIsVerified(x db.EngineMigration) error {
	type GPGKey struct {
		Verified bool `xorm:"NOT NULL DEFAULT false"`
	}

	return x.Sync(new(GPGKey))
}
