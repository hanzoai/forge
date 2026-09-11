// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package swagger

import (
	api "github.com/hanzoai/git/modules/structs"
)

// ActivityFeedsList
// swagger:response ActivityFeedsList
type swaggerActivityFeedsList struct {
	// in:body
	Body []api.Activity `json:"body"`
}
