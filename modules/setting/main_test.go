// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// This package cannot use SetupGitTestEnv: it *is* the setting package, and
	// the path tests drive InitWorkPathAndCommonConfig themselves. Raise the flag
	// directly so the server-process guards inside it (mustNotRunAsRoot) know they
	// are looking at a test binary.
	IsInTesting = true
	os.Exit(m.Run())
}
