// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name     string
		toEscape string
		want     string
	}{
		{
			"Simplest case - nothing to escape",
			"a/b/c/d",
			"a/b/c/d",
		}, {
			"Prefixed tilde - with normal stuff - should not escape",
			"~/src/go/forge/forge",
			"~/src/go/forge/forge",
		}, {
			"Typical windows path with spaces - should get doublequote escaped",
			`C:\Program Files\Forge v1.13 - I like lots of spaces\forge`,
			`"C:\\Program Files\\Forge v1.13 - I like lots of spaces\\forge"`,
		}, {
			"Forward-slashed windows path with spaces - should get doublequote escaped",
			"C:/Program Files/Forge v1.13 - I like lots of spaces/forge",
			`"C:/Program Files/Forge v1.13 - I like lots of spaces/forge"`,
		}, {
			"Prefixed tilde - but then a space filled path",
			"~git/Forge v1.13/forge",
			`~git/"Forge v1.13/forge"`,
		}, {
			"Bangs are unfortunately not predictable so need to be singlequoted",
			"C:/Program Files/Forge!/forge",
			`'C:/Program Files/Forge!/forge'`,
		}, {
			"Newlines are just irritating",
			"/home/git/Forge\n\nWHY-WOULD-YOU-DO-THIS\n\nHanzo/forge",
			"'/home/git/Forge\n\nWHY-WOULD-YOU-DO-THIS\n\nHanzo/forge'",
		}, {
			"Similarly we should nicely handle multiple single quotes if we have to single-quote",
			"'!''!'''!''!'!'",
			`\''!'\'\''!'\'\'\''!'\'\''!'\''!'\'`,
		}, {
			"Double quote < ...",
			"~/<forge",
			"~/\"<forge\"",
		}, {
			"Double quote > ...",
			"~/forge>",
			"~/\"forge>\"",
		}, {
			"Double quote and escape $ ...",
			"~/$forge",
			"~/\"\\$forge\"",
		}, {
			"Double quote {...",
			"~/{forge",
			"~/\"{forge\"",
		}, {
			"Double quote }...",
			"~/forge}",
			"~/\"forge}\"",
		}, {
			"Double quote ()...",
			"~/(forge)",
			"~/\"(forge)\"",
		}, {
			"Double quote and escape `...",
			"~/forge`",
			"~/\"forge\\`\"",
		}, {
			"Double quotes can handle a number of things without having to escape them but not everything ...",
			"~/<forge> ${forge} `forge` [forge] (forge) \"forge\" \\forge\\ 'forge'",
			"~/\"<forge> \\${forge} \\`forge\\` [forge] (forge) \\\"forge\\\" \\\\forge\\\\ 'forge'\"",
		}, {
			"Single quotes don't need to escape except for '...",
			"~/<forge> ${forge} `forge` (forge) !forge! \"forge\" \\forge\\ 'forge'",
			"~/'<forge> ${forge} `forge` (forge) !forge! \"forge\" \\forge\\ '\\''forge'\\'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ShellEscape(tt.toEscape))
		})
	}
}
