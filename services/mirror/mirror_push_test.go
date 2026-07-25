// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSameRemote(t *testing.T) {
	// The same target, spelled the ways a remote gets written.
	assert.True(t, sameRemote("https://github.com/o/r.git", "https://github.com/o/r"))
	assert.True(t, sameRemote("https://tok@github.com/o/r.git", "https://github.com/o/r.git"), "credentials ignored")
	assert.True(t, sameRemote("https://GitHub.com/o/r", "https://github.com/o/r"), "host case-insensitive")
	assert.True(t, sameRemote("git@github.com:o/r.git", "https://github.com/o/r.git"), "scp form is the same repo")

	assert.False(t, sameRemote("https://github.com/o/r", "https://gitlab.com/o/r"), "different host")
	assert.False(t, sameRemote("https://github.com/o/r", "https://github.com/o/other"), "different repo")
	assert.False(t, sameRemote("", "https://github.com/o/r"))
	assert.False(t, sameRemote("not a url", "also not"))
}
