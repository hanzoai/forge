// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSwaggerURL(t *testing.T) {
	defer func(v string) { API.SwaggerURL = v }(API.SwaggerURL)

	API.SwaggerURL = ""
	cfg, err := NewConfigProviderFromData("[server]\nROOT_URL = https://forge.hanzo.ai/\n")
	assert.NoError(t, err)
	loadAPIFrom(cfg)
	assert.Equal(t, "https://forge.hanzo.ai/swagger.v1.json", API.SwaggerURL)

	API.SwaggerURL = ""
	cfg, err = NewConfigProviderFromData("[server]\nROOT_URL = https://git.hanzo.ai/\n[api]\nSWAGGER_URL = https://forge.hanzo.ai/swagger\n")
	assert.NoError(t, err)
	loadAPIFrom(cfg)
	assert.Equal(t, "https://forge.hanzo.ai/swagger", API.SwaggerURL, "an explicit SWAGGER_URL is honored")
}
