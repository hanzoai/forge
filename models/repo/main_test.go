// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	_ "github.com/hanzoai/git/models" // register table model
	_ "github.com/hanzoai/git/models/actions"
	_ "github.com/hanzoai/git/models/activities"
	_ "github.com/hanzoai/git/models/perm/access" // register table model
	_ "github.com/hanzoai/git/models/repo"        // register table model
	_ "github.com/hanzoai/git/models/user"        // register table model
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
