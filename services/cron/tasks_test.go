// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// What a registered job declares to Hanzo Tasks. The predecessor of this test
// asserted that gocron held a tagged job; that scheduler is gone, and the thing
// worth pinning now is the shape of the schedule itself — its id addresses one
// row across restarts, and its input is what tells the one workflow type which
// job to run.
func TestScheduleOptionsFor(t *testing.T) {
	opts := scheduleOptionsFor(&Task{
		Name:   "task 1",
		config: &BaseConfig{Schedule: "5 4 * * *"},
	})

	assert.Equal(t, "git-cron-task 1", opts.ID)
	assert.Equal(t, []string{"5 4 * * *"}, opts.CronExpressions)
	assert.Equal(t, workflowType, opts.WorkflowType)
	assert.Equal(t, taskQueue, opts.TaskQueue)
	assert.Equal(t, []any{"task 1"}, opts.Input)
}

// A second job is a second schedule, not a second scheduler: same workflow
// type, same queue, distinguished only by id and input.
func TestScheduleOptionsAreDistinctPerJob(t *testing.T) {
	a := scheduleOptionsFor(&Task{Name: "alpha", config: &BaseConfig{Schedule: "@daily"}})
	b := scheduleOptionsFor(&Task{Name: "beta", config: &BaseConfig{Schedule: "@hourly"}})

	assert.NotEqual(t, a.ID, b.ID)
	assert.Equal(t, a.WorkflowType, b.WorkflowType)
	assert.Equal(t, a.TaskQueue, b.TaskQueue)
	assert.Equal(t, []any{"alpha"}, a.Input)
	assert.Equal(t, []any{"beta"}, b.Input)
}
