// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_16

import "github.com/hanzoai/git/models/db"

func AddWebAuthnCred(x db.EngineMigration) error {
	// NO-OP Don't migrate here - let v210 do this.

	return nil
}
