// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package access_test

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	_ "github.com/hanzoai/git/models"
	_ "github.com/hanzoai/git/models/actions"
	_ "github.com/hanzoai/git/models/activities"
	_ "github.com/hanzoai/git/models/repo"
	_ "github.com/hanzoai/git/models/user"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
