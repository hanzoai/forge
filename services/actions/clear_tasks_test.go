// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"
	"time"

	actions_model "github.com/hanzoai/git/models/actions"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/queue"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createConflictingCancellingJob(t *testing.T, concurrencyGroup string, runIndex int64) *actions_model.ActionRunJob {
	t.Helper()

	run := &actions_model.ActionRun{
		RepoID:        1,
		OwnerID:       2,
		TriggerUserID: 2,
		WorkflowID:    "test.yml",
		Index:         runIndex,
		Ref:           "refs/heads/main",
		Status:        actions_model.StatusBlocked,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	attempt := &actions_model.ActionRunAttempt{
		RepoID:           run.RepoID,
		RunID:            run.ID,
		Attempt:          1,
		TriggerUserID:    run.TriggerUserID,
		Status:           actions_model.StatusBlocked,
		ConcurrencyGroup: concurrencyGroup,
	}
	require.NoError(t, db.Insert(t.Context(), attempt))

	job := &actions_model.ActionRunJob{
		RunID:            run.ID,
		RunAttemptID:     attempt.ID,
		AttemptJobID:     1,
		RepoID:           run.RepoID,
		OwnerID:          run.OwnerID,
		CommitSHA:        "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Name:             "conflicting-cancelling-job",
		JobID:            "conflicting-cancelling-job",
		Status:           actions_model.StatusCancelling,
		ConcurrencyGroup: concurrencyGroup,
	}
	require.NoError(t, db.Insert(t.Context(), job))

	return job
}

func TestShouldBlockJobByConcurrency_CancellingJobBlocks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	const concurrencyGroup = "test-cancelling-job-blocks"
	createConflictingCancellingJob(t, concurrencyGroup, 9903)

	job := &actions_model.ActionRunJob{
		RepoID:                 1,
		RawConcurrency:         concurrencyGroup,
		IsConcurrencyEvaluated: true,
		ConcurrencyGroup:       concurrencyGroup,
	}

	shouldBlock, err := shouldBlockJobByConcurrency(t.Context(), job)
	require.NoError(t, err)
	assert.True(t, shouldBlock)
}

func TestShouldBlockRunByConcurrency_CancellingJobBlocks(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	const concurrencyGroup = "test-cancelling-run-blocks"
	createConflictingCancellingJob(t, concurrencyGroup, 9904)

	attempt := &actions_model.ActionRunAttempt{
		RepoID:           1,
		ConcurrencyGroup: concurrencyGroup,
	}

	shouldBlock, err := shouldBlockRunByConcurrency(t.Context(), attempt)
	require.NoError(t, err)
	assert.True(t, shouldBlock)
}

// The repo-1 runner fixture carries these labels; anything else is unroutable
// on the test instance.
const (
	satisfiableLabel   = "runner_to_be_deleted"
	unsatisfiableLabel = "no-runner-carries-this"
)

func createWaitingJob(t *testing.T, runIndex int64, name string, runsOn []string, taskID int64) *actions_model.ActionRunJob {
	t.Helper()

	run := &actions_model.ActionRun{
		RepoID:        1,
		OwnerID:       2,
		TriggerUserID: 2,
		WorkflowID:    "test.yml",
		Index:         runIndex,
		Ref:           "refs/heads/main",
		Status:        actions_model.StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	attempt := &actions_model.ActionRunAttempt{
		RepoID:        run.RepoID,
		RunID:         run.ID,
		Attempt:       1,
		TriggerUserID: run.TriggerUserID,
		Status:        actions_model.StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), attempt))

	job := &actions_model.ActionRunJob{
		RunID:        run.ID,
		RunAttemptID: attempt.ID,
		AttemptJobID: 1,
		RepoID:       run.RepoID,
		OwnerID:      run.OwnerID,
		CommitSHA:    "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Name:         name,
		JobID:        name,
		Status:       actions_model.StatusWaiting,
		RunsOn:       runsOn,
		TaskID:       taskID,
	}
	require.NoError(t, db.Insert(t.Context(), job))

	return job
}

func jobStatus(t *testing.T, id int64) actions_model.Status {
	t.Helper()
	job, err := actions_model.GetRunJobByRepoAndID(t.Context(), 1, id)
	require.NoError(t, err)
	return job.Status
}

// The reaper notifies the job emitter about what it changed, and that queue is
// built by Init() which unit tests do not run. Build it here so the tests
// exercise the whole function rather than a truncated version of it.
func initJobEmitter(t *testing.T) {
	t.Helper()
	if jobEmitterQueue == nil {
		jobEmitterQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "actions_ready_job", jobEmitterQueueHandler)
	}
	require.NotNil(t, jobEmitterQueue, "job emitter queue must exist for these tests to mean anything")
}

// withGrace runs f with the unsatisfiable-job grace set to d. A negative grace
// puts the cutoff in the future so freshly-inserted jobs are in scope, which is
// how these tests avoid backdating rows.
func withGrace(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	initJobEmitter(t)
	prev := setting.Actions.UnsatisfiableJobGrace
	setting.Actions.UnsatisfiableJobGrace = d
	defer func() { setting.Actions.UnsatisfiableJobGrace = prev }()
	f()
}

// The point of the reaper: a job asking for a label nothing on the instance
// carries can never be picked, so it fails rather than waiting forever.
func TestFailUnsatisfiableJobs_FailsJobNoRunnerCanMatch(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	job := createWaitingJob(t, 9910, "wants-a-phantom-label", []string{unsatisfiableLabel}, 0)

	withGrace(t, -time.Hour, func() {
		require.NoError(t, FailUnsatisfiableJobs(t.Context()))
	})

	assert.Equal(t, actions_model.StatusFailure, jobStatus(t, job.ID),
		"a job whose labels no registered runner carries must fail, not wait")
}

// The negative control, and the one that matters most: a reaper that eats
// routable work is worse than the backlog it was written to prevent. This job is
// old enough to be in scope and IS matched by a registered runner, so it must be
// left alone no matter how long it has waited — a busy fleet is not a broken one.
func TestFailUnsatisfiableJobs_LeavesRoutableJobWaiting(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	job := createWaitingJob(t, 9911, "wants-a-real-label", []string{satisfiableLabel}, 0)

	withGrace(t, -time.Hour, func() {
		require.NoError(t, FailUnsatisfiableJobs(t.Context()))
	})

	assert.Equal(t, actions_model.StatusWaiting, jobStatus(t, job.ID),
		"a job a registered runner can match must keep waiting for it")
}

// Registration is the test, not liveness. The fixture runners are all long
// offline, and their jobs must still be safe: an offline runner is coming back,
// and failing its queue during a fleet rollout would be the worse bug.
func TestFailUnsatisfiableJobs_OfflineRunnerStillCounts(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	runners, err := db.Find[actions_model.ActionRunner](t.Context(), actions_model.FindRunnerOptions{
		RepoID:        1,
		WithAvailable: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, runners, "fixture must provide a runner reachable from repo 1")

	var online int
	for _, r := range runners {
		if r.IsOnline() {
			online++
		}
	}
	require.Zero(t, online, "this test is only meaningful while every fixture runner is offline")

	job := createWaitingJob(t, 9912, "waits-for-an-offline-runner", []string{satisfiableLabel}, 0)

	withGrace(t, -time.Hour, func() {
		require.NoError(t, FailUnsatisfiableJobs(t.Context()))
	})

	assert.Equal(t, actions_model.StatusWaiting, jobStatus(t, job.ID),
		"an offline but registered runner must keep its jobs alive")
}

// The grace period exists so a job created just before its runner registers is
// not killed in the gap. Within it, even an unroutable job is untouched.
func TestFailUnsatisfiableJobs_GraceProtectsYoungJobs(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	job := createWaitingJob(t, 9913, "young-and-unroutable", []string{unsatisfiableLabel}, 0)

	withGrace(t, time.Hour, func() {
		require.NoError(t, FailUnsatisfiableJobs(t.Context()))
	})

	assert.Equal(t, actions_model.StatusWaiting, jobStatus(t, job.ID),
		"a job younger than the grace period must be given time to find its runner")
}

// A job with no labels is unrouted, not unroutable — a different fault, and the
// abandoned sweep's business. Failing it here would mean this reaper decides
// something it has no evidence about.
func TestFailUnsatisfiableJobs_IgnoresJobWithNoLabels(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	job := createWaitingJob(t, 9914, "no-labels-at-all", nil, 0)

	withGrace(t, -time.Hour, func() {
		require.NoError(t, FailUnsatisfiableJobs(t.Context()))
	})

	assert.Equal(t, actions_model.StatusWaiting, jobStatus(t, job.ID),
		"a job with no runs-on labels is not this reaper's to judge")
}

// The write is guarded on (task_id = 0, waiting) so a runner claiming the job in
// the same moment wins the race. Standing in for that runner: a job already
// carrying a task id must survive even though its labels are unroutable.
func TestFailWaitingJobs_LeavesClaimedJobAlone(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	job := createWaitingJob(t, 9915, "already-claimed", []string{unsatisfiableLabel}, 12345)

	failed, err := actions_model.FailWaitingJobs(t.Context(), []*actions_model.ActionRunJob{job})
	require.NoError(t, err)

	assert.Empty(t, failed, "a claimed job must not be reported as failed")
	assert.Equal(t, actions_model.StatusWaiting, jobStatus(t, job.ID),
		"a job a runner has claimed must not be failed out from under it")
}

func withZombieTimeout(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	initJobEmitter(t)
	prev := setting.Actions.ZombieTaskTimeout
	setting.Actions.ZombieTaskTimeout = d
	defer func() { setting.Actions.ZombieTaskTimeout = prev }()
	f()
}

// newRunningTask builds a running job holding a running task. stepStarted stamps
// its one step as having begun, which is the difference between a task that had
// done nothing when it was stopped and one that was part-way through real work.
func newRunningTask(t *testing.T, name string, runIndex int64, stepStarted bool) (*actions_model.ActionTask, *actions_model.ActionRunJob) {
	t.Helper()

	run := &actions_model.ActionRun{
		Title:         name,
		RepoID:        1,
		OwnerID:       2,
		TriggerUserID: 2,
		WorkflowID:    "test.yml",
		Index:         runIndex,
		Ref:           "refs/heads/main",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Status:        actions_model.StatusRunning,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	job := &actions_model.ActionRunJob{
		RunID:     run.ID,
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
		Name:      name,
		JobID:     name,
		Attempt:   1,
		Status:    actions_model.StatusRunning,
		Started:   timeutil.TimeStampNow(),
	}
	require.NoError(t, db.Insert(t.Context(), job))

	runner := &actions_model.ActionRunner{
		UUID: "runner-" + name,
		Name: "runner-" + name,
	}
	require.NoError(t, db.Insert(t.Context(), runner))

	task := &actions_model.ActionTask{
		JobID:     job.ID,
		Attempt:   1,
		RunnerID:  runner.ID,
		Status:    actions_model.StatusRunning,
		Started:   timeutil.TimeStampNow(),
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
	}
	require.NoError(t, db.Insert(t.Context(), task))

	step := &actions_model.ActionTaskStep{
		Name:   "Run true",
		TaskID: task.ID,
		Index:  0,
		RepoID: task.RepoID,
		Status: actions_model.StatusWaiting,
	}
	if stepStarted {
		step.Started = timeutil.TimeStampNow()
		step.Status = actions_model.StatusRunning
	}
	require.NoError(t, db.Insert(t.Context(), step))

	job.TaskID = task.ID
	_, err := actions_model.UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	return task, job
}

// A runner pod that goes away without a word leaves its task running until the
// zombie sweep finds it. If it went before any step began, the work is still
// undone and untouched, so the job is handed to another runner.
func TestStopZombieTasksRequeuesDroppedTask(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	task, job := newRunningTask(t, "runner-vanished", 9930, false)

	withZombieTimeout(t, -time.Hour, func() {
		require.NoError(t, StopZombieTasks(t.Context()))
	})

	after := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
	assert.Equal(t, actions_model.StatusWaiting, after.Status, "a job whose runner died before it ran anything belongs back in the queue")
	assert.Zero(t, after.TaskID)
	assert.EqualValues(t, 1, after.Drops)

	// Stopping the task must not settle a job that has moved on.
	afterTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
	assert.Equal(t, actions_model.StatusFailure, afterTask.Status, "the task itself still stops")
}

// A runner that died part-way through real work leaves a job that may have done
// something. Re-running it could repeat that, so the failure stands.
func TestStopZombieTasksKeepsPartlyRunJobFailed(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	_, job := newRunningTask(t, "runner-died-mid-step", 9931, true)

	withZombieTimeout(t, -time.Hour, func() {
		require.NoError(t, StopZombieTasks(t.Context()))
	})

	after := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
	assert.Equal(t, actions_model.StatusFailure, after.Status, "a job whose step began must not be silently run again")
	assert.Zero(t, after.Drops)
}

func withEndlessTimeout(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	initJobEmitter(t)
	prev := setting.Actions.EndlessTaskTimeout
	setting.Actions.EndlessTaskTimeout = d
	defer func() { setting.Actions.EndlessTaskTimeout = prev }()
	f()
}

// The endless sweep selects on start time alone, so what it finds is a task whose
// runner is alive and has been reporting for hours. That job is wedged, not lost:
// handing it back would spend hours more of a shared runner arriving at the same
// wedge, so it fails where it stands.
func TestStopEndlessTasksKeepsWedgedJobFailed(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	_, job := newRunningTask(t, "runner-still-talking", 9932, false)

	withEndlessTimeout(t, -time.Hour, func() {
		require.NoError(t, StopEndlessTasks(t.Context()))
	})

	after := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
	assert.Equal(t, actions_model.StatusFailure, after.Status, "a wedged job is not a dropped one")
	assert.Zero(t, after.Drops, "an endless task must not spend a drop")
}
