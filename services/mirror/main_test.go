// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"testing"

	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/setting"
	repo_service "github.com/hanzoai/git/services/repository"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m, &unittest.TestOptions{
		SetUp: func() error {
			// announce queues a license scan, which needs the repository queues running.
			setting.LoadQueueSettings()
			return repo_service.Init(context.Background())
		},
	})
}
