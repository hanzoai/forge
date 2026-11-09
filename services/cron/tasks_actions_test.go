// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"testing"
	"time"

	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without Hanzo Tasks nothing fires wake_runners, so a lost runner wakeup is never repaired.
// Boot must say so at error level instead of leaving it to be discovered.
func TestWarnWakeRunnersUnscheduled(t *testing.T) {
	require.Nil(t, tasksClient)

	lc, cleanup := test.NewLogChecker(log.DEFAULT)
	lc.Filter("wake_runners cannot be scheduled", "will not be repaired").StopMark("logs-done")
	defer cleanup()

	warnWakeRunnersUnscheduled()

	log.Error("logs-done") // events are read in order, so everything logged before this has been seen

	seen, _ := lc.Check(5 * time.Second)
	assert.True(t, seen[0], "must name the task")
	assert.True(t, seen[1], "must say what is lost")
}
