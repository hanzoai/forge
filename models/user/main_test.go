// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user_test

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	_ "github.com/hanzoai/git/models"
	_ "github.com/hanzoai/git/models/actions"
	_ "github.com/hanzoai/git/models/activities"
	_ "github.com/hanzoai/git/models/user"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
