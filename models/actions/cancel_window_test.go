// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"

	"github.com/hanzoai/git/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cancelling a job stops its task, and StopTask writes the job before it writes
// the task, so for that moment the job reads Cancelling while its task still
// reads Running. A sweep landing in the window derives its intent from the task,
// reads Running, and offers the job for re-queue. The status leg of
// RequeueDroppedTask's guard is the only thing that refuses it, which is why that
// guard is tighter than the other two.
func TestRequeueRefusesCancellingJob(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "cancelling-window", 9950, 0)

	// The job has been told to cancel; the task has not caught up yet.
	job.Status = StatusCancelling
	_, err := UpdateRunJob(t.Context(), job, nil, "status")
	require.NoError(t, err)

	requeued, err := RequeueDroppedTask(t.Context(), task)
	require.NoError(t, err)
	assert.False(t, requeued, "a cancel must never be undone by a re-queue")

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusCancelling, after.Status, "the cancel must stand")
	assert.Equal(t, task.ID, after.TaskID)
	assert.Zero(t, after.Drops, "a refused re-queue must not spend the budget")
}

// The inverse hazard of that tightness. A task settling the job it still owns has
// to work from Cancelling too, not only from Running. If StopTask's guard ever
// grew a status leg the way RequeueDroppedTask's has, this job would sit in
// Cancelling for good: no sweep revisits a Cancelling job whose task is done.
func TestStopTaskSettlesItsOwnCancellingJob(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "cancel-settles", 9951, 0)

	// First half of a cancel: both the job and the task reach Cancelling.
	require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelling))
	require.Equal(t, StatusCancelling,
		unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID}).Status)

	// Second half: the task finishes cancelling and settles the job it still owns.
	require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelled))

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusCancelled, after.Status,
		"a task settling the job it still owns must not be refused, or the job strands in Cancelling")
}
