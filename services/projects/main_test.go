// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	_ "github.com/hanzoai/git/models/actions"
	_ "github.com/hanzoai/git/models/activities"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
