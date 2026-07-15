// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	_ "github.com/hanzoai/git/models/actions"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
