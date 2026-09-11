// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"net/http"

	"github.com/hanzoai/git/services/context"
)

func Swagger(ctx *context.Context) {
	ctx.HTML(http.StatusOK, "swagger/openapi-viewer")
}
