// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webtheme

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseThemeMetaInfo(t *testing.T) {
	m := parseThemeMetaInfoToMap(`hanzo-theme-meta-info {
	--k1: "v1";
	--k2: "v\"2";
	--k3: 'v3';
	--k4: 'v\'4';
	--k5: v5;
}`)
	assert.Equal(t, map[string]string{
		"--k1": "v1",
		"--k2": `v"2`,
		"--k3": "v3",
		"--k4": "v'4",
		"--k5": "v5",
	}, m)

	// if an auto theme imports others, the meta info should be extracted from the last one
	// the meta in imported themes should be ignored to avoid incorrect overriding
	m = parseThemeMetaInfoToMap(`
@media (prefers-color-scheme: dark) { hanzo-theme-meta-info { --k1: foo; } }
@media (prefers-color-scheme: light) { hanzo-theme-meta-info { --k1: bar; } }
hanzo-theme-meta-info {
	--k2: real;
}`)
	assert.Equal(t, map[string]string{"--k2": "real"}, m)

	// compressed CSS, no trailing semicolon
	m = parseThemeMetaInfoToMap(`hanzo-theme-meta-info{--k1:"v1"}`)
	assert.Equal(t, map[string]string{"--k1": "v1"}, m)
	m = parseThemeMetaInfoToMap(`hanzo-theme-meta-info{--k1:"v1";--k2:"v2"}`)
	assert.Equal(t, map[string]string{"--k1": "v1", "--k2": "v2"}, m)
}
