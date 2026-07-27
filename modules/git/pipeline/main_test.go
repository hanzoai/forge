// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pipeline

import (
	"testing"

	"github.com/hanzoai/git/modules/git"
)

func TestMain(m *testing.M) {
	git.RunGitTests(m)
}
