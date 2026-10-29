// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/url"
	"path"

	"github.com/hanzoai/git/modules/log"
)

// API settings
var API = struct {
	EnableSwagger          bool
	SwaggerURL             string `ini:"SWAGGER_URL"`
	MaxResponseItems       int
	DefaultPagingNum       int
	DefaultGitTreesPerPage int
	DefaultMaxBlobSize     int64
	DefaultMaxResponseSize int64
}{
	EnableSwagger:          true,
	SwaggerURL:             "",
	MaxResponseItems:       50,
	DefaultPagingNum:       30,
	DefaultGitTreesPerPage: 1000,
	DefaultMaxBlobSize:     10485760,
	DefaultMaxResponseSize: 104857600,
}

func loadAPIFrom(rootCfg ConfigProvider) {
	mustMapSetting(rootCfg, "api", &API)

	defaultAppURL := string(Protocol) + "://" + Domain + ":" + HTTPPort
	u, err := url.Parse(rootCfg.Section("server").Key("ROOT_URL").MustString(defaultAppURL))
	if err != nil {
		log.Fatal("Invalid ROOT_URL '%s': %s", AppURL, err)
	}
	// An explicit [api] SWAGGER_URL wins. It used to be overwritten here
	// unconditionally with ROOT_URL + "/api/swagger" — a path this fork does not
	// serve (the UI is /swagger and the spec /swagger.v1.json, routers/web/web.go)
	// and, with ROOT_URL on the legacy git.hanzo.ai host, the wrong host too. Every
	// API error body links this, so a wrong value sends every caller to a 404.
	if API.SwaggerURL != "" {
		return
	}
	u.Path = path.Join(u.Path, "swagger.v1.json")
	API.SwaggerURL = u.String()
}
