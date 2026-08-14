// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"strings"
	"testing"
	"time"

	runnerv1 "github.com/hanzo-git/actions-proto-go/runner/v1"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/actions/jobparser"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMakeTaskStepDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		jobStep  *jobparser.Step
		expected string
	}{
		{
			name: "explicit name",
			jobStep: &jobparser.Step{
				Name: "Test Step",
			},
			expected: "Test Step",
		},
		{
			name: "uses step",
			jobStep: &jobparser.Step{
				Uses: "actions/checkout@v4",
			},
			expected: "Run actions/checkout@v4",
		},
		{
			name: "single-line run",
			jobStep: &jobparser.Step{
				Run: "echo hello",
			},
			expected: "Run echo hello",
		},
		{
			name: "multi-line run block scalar",
			jobStep: &jobparser.Step{
				Run: "\n  echo hello  \r\n  echo world  \n  ",
			},
			expected: "Run echo hello",
		},
		{
			name: "fallback to id",
			jobStep: &jobparser.Step{
				ID: "step-id",
			},
			expected: "Run step-id",
		},
		{
			name: "very long name truncated",
			jobStep: &jobparser.Step{
				Name: strings.Repeat("a", 300),
			},
			expected: strings.Repeat("a", 252) + "…",
		},
		{
			name: "very long run truncated",
			jobStep: &jobparser.Step{
				Run: strings.Repeat("a", 300),
			},
			expected: "Run " + strings.Repeat("a", 248) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := makeTaskStepDisplayName(tt.jobStep, 255)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTaskCancellingFinalizesToCancelled(t *testing.T) {
	newRunningTask := func(t *testing.T) (*ActionTask, *ActionRunJob) {
		t.Helper()

		run := &ActionRun{
			Title:         "cancelling-test-run",
			RepoID:        1,
			OwnerID:       2,
			WorkflowID:    "test.yaml",
			Index:         999,
			TriggerUserID: 2,
			Ref:           "refs/heads/master",
			CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
			Event:         "push",
			TriggerEvent:  "push",
			Status:        StatusRunning,
			Started:       timeutil.TimeStampNow(),
		}
		require.NoError(t, db.Insert(t.Context(), run))

		job := &ActionRunJob{
			RunID:     run.ID,
			RepoID:    run.RepoID,
			OwnerID:   run.OwnerID,
			CommitSHA: run.CommitSHA,
			Name:      "cancelling-finalization-job",
			Attempt:   1,
			JobID:     "cancelling-finalization-job",
			Status:    StatusRunning,
		}
		require.NoError(t, db.Insert(t.Context(), job))

		runner := &ActionRunner{
			UUID:                 "runner-cancelling-supported",
			Name:                 "runner-cancelling-supported",
			HasCancellingSupport: true,
		}
		require.NoError(t, db.Insert(t.Context(), runner))

		task := &ActionTask{
			JobID:     job.ID,
			Attempt:   1,
			RunnerID:  runner.ID,
			Status:    StatusRunning,
			Started:   timeutil.TimeStampNow(),
			RepoID:    run.RepoID,
			OwnerID:   run.OwnerID,
			CommitSHA: run.CommitSHA,
		}
		require.NoError(t, db.Insert(t.Context(), task))

		job.TaskID = task.ID
		_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
		require.NoError(t, err)

		return task, job
	}

	testResult := func(t *testing.T, result runnerv1.Result) {
		t.Helper()
		require.NoError(t, unittest.PrepareTestDatabase())

		task, job := newRunningTask(t)
		require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelling))

		taskAfterStop := unittest.AssertExistsAndLoadBean(t, &ActionTask{ID: task.ID})
		assert.Equal(t, StatusCancelling, taskAfterStop.Status)

		updatedTask, err := UpdateTaskByState(t.Context(), task.RunnerID, &runnerv1.TaskState{
			Id:        task.ID,
			Result:    result,
			StoppedAt: timestamppb.Now(),
		})
		require.NoError(t, err)
		assert.Equal(t, StatusCancelled, updatedTask.Status)

		taskAfterUpdate := unittest.AssertExistsAndLoadBean(t, &ActionTask{ID: task.ID})
		assert.Equal(t, StatusCancelled, taskAfterUpdate.Status)

		jobAfterUpdate := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
		assert.Equal(t, StatusCancelled, jobAfterUpdate.Status)
	}

	t.Run("runner reports success", func(t *testing.T) {
		testResult(t, runnerv1.Result_RESULT_SUCCESS)
	})

	t.Run("runner reports failure", func(t *testing.T) {
		testResult(t, runnerv1.Result_RESULT_FAILURE)
	})
}

func TestStopTaskCancellingFallsBackForLegacyRunner(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	run := &ActionRun{
		Title:         "cancelling-test-run",
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         999,
		TriggerUserID: 2,
		Ref:           "refs/heads/master",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusRunning,
		Started:       timeutil.TimeStampNow(),
	}
	require.NoError(t, db.Insert(t.Context(), run))

	job := &ActionRunJob{
		RunID:     run.ID,
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
		Name:      "legacy-cancelling-job",
		Attempt:   1,
		JobID:     "legacy-cancelling-job",
		Status:    StatusRunning,
	}
	require.NoError(t, db.Insert(t.Context(), job))

	runner := &ActionRunner{
		UUID:                 "runner-legacy-no-cancelling",
		Name:                 "runner-legacy-no-cancelling",
		HasCancellingSupport: false,
	}
	require.NoError(t, db.Insert(t.Context(), runner))

	task := &ActionTask{
		JobID:     job.ID,
		Attempt:   1,
		RunnerID:  runner.ID,
		Status:    StatusRunning,
		Started:   timeutil.TimeStampNow(),
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
	}
	require.NoError(t, db.Insert(t.Context(), task))

	job.TaskID = task.ID
	_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelling))

	taskAfterStop := unittest.AssertExistsAndLoadBean(t, &ActionTask{ID: task.ID})
	assert.Equal(t, StatusCancelled, taskAfterStop.Status)
	assert.NotZero(t, taskAfterStop.Stopped)

	jobAfterStop := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusCancelled, jobAfterStop.Status)
	assert.NotZero(t, jobAfterStop.Stopped)
}

func TestStopTaskCancellingFallsBackForMissingRunner(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	run := &ActionRun{
		Title:         "cancelling-test-run",
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         999,
		TriggerUserID: 2,
		Ref:           "refs/heads/master",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusRunning,
		Started:       timeutil.TimeStampNow(),
	}
	require.NoError(t, db.Insert(t.Context(), run))

	job := &ActionRunJob{
		RunID:     run.ID,
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
		Name:      "missing-runner-cancelling-job",
		Attempt:   1,
		JobID:     "missing-runner-cancelling-job",
		Status:    StatusRunning,
	}
	require.NoError(t, db.Insert(t.Context(), job))

	runner := &ActionRunner{
		UUID:                 "runner-cleaned-up-before-cancel",
		Name:                 "runner-cleaned-up-before-cancel",
		HasCancellingSupport: true,
	}
	require.NoError(t, db.Insert(t.Context(), runner))

	task := &ActionTask{
		JobID:     job.ID,
		Attempt:   1,
		RunnerID:  runner.ID,
		Status:    StatusRunning,
		Started:   timeutil.TimeStampNow(),
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
	}
	require.NoError(t, db.Insert(t.Context(), task))

	job.TaskID = task.ID
	_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	_, err = db.DeleteByID[ActionRunner](t.Context(), runner.ID)
	require.NoError(t, err)

	require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelling))

	taskAfterStop := unittest.AssertExistsAndLoadBean(t, &ActionTask{ID: task.ID})
	assert.Equal(t, StatusCancelled, taskAfterStop.Status)
	assert.NotZero(t, taskAfterStop.Stopped)

	jobAfterStop := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusCancelled, jobAfterStop.Status)
	assert.NotZero(t, jobAfterStop.Stopped)
}

// TestReleaseTaskForRunner verifies that releasing a freshly-claimed task returns
// its job to the waiting queue and deletes the task and its steps, so a failure
// while assembling the runner response cannot strand the job in running state.
func TestReleaseTaskForRunner(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	run := &ActionRun{
		Title:         "release-task-test-run",
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         9902,
		TriggerUserID: 2,
		Ref:           "refs/heads/main",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	job := &ActionRunJob{
		RunID:           run.ID,
		RepoID:          run.RepoID,
		OwnerID:         run.OwnerID,
		CommitSHA:       run.CommitSHA,
		Name:            "release-job",
		Attempt:         1,
		JobID:           "release-job",
		Status:          StatusWaiting,
		RunsOn:          []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: push\njobs:\n  release-job:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"),
	}
	require.NoError(t, db.Insert(t.Context(), job))

	runner := &ActionRunner{
		UUID:        "release-runner-uuid",
		Name:        "release-runner",
		AgentLabels: []string{"ubuntu-latest"},
	}
	runner.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), runner))

	task, ok, err := CreateTaskForRunner(t.Context(), runner)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, task)

	claimed := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	require.Equal(t, StatusRunning, claimed.Status)
	require.Equal(t, task.ID, claimed.TaskID)

	require.NoError(t, ReleaseTaskForRunner(t.Context(), task))

	// Job is back in the waiting queue with no task assigned.
	released := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusWaiting, released.Status)
	assert.Zero(t, released.TaskID)
	assert.Zero(t, released.Started)

	// The task and its steps are gone.
	unittest.AssertNotExistsBean(t, &ActionTask{ID: task.ID})
	unittest.AssertNotExistsBean(t, &ActionTaskStep{TaskID: task.ID})
}

// TestCreateTaskForRunnerPagination verifies that a job sitting beyond the first page is still claimed
func TestCreateTaskForRunnerPagination(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	defer func(orig int) { pickTaskBatchSize = orig }(pickTaskBatchSize)
	pickTaskBatchSize = 2

	run := &ActionRun{
		Title:         "pagination-test-run",
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         9903,
		TriggerUserID: 2,
		Ref:           "refs/heads/main",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	// Five waiting jobs the runner cannot run, then one it can.
	// With a page size of 2 the matching job only appears on the third page.
	for i := range 5 {
		mismatch := &ActionRunJob{
			RunID:     run.ID,
			RepoID:    run.RepoID,
			OwnerID:   run.OwnerID,
			CommitSHA: run.CommitSHA,
			Name:      "mismatch-" + string(rune('a'+i)),
			Attempt:   1,
			JobID:     "mismatch-" + string(rune('a'+i)),
			Status:    StatusWaiting,
			RunsOn:    []string{"windows-latest"},
		}
		require.NoError(t, db.Insert(t.Context(), mismatch))
	}

	target := &ActionRunJob{
		RunID:           run.ID,
		RepoID:          run.RepoID,
		OwnerID:         run.OwnerID,
		CommitSHA:       run.CommitSHA,
		Name:            "target-job",
		Attempt:         1,
		JobID:           "target-job",
		Status:          StatusWaiting,
		RunsOn:          []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: push\njobs:\n  target-job:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"),
	}
	require.NoError(t, db.Insert(t.Context(), target))

	runner := &ActionRunner{
		UUID:        "pagination-runner-uuid",
		Name:        "pagination-runner",
		AgentLabels: []string{"ubuntu-latest"},
	}
	runner.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), runner))

	task, ok, err := CreateTaskForRunner(t.Context(), runner)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, task)

	claimed := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: target.ID})
	assert.Equal(t, StatusRunning, claimed.Status)
	assert.Equal(t, task.ID, claimed.TaskID)
}

// A job whose stored workflow cannot be parsed must fail ITSELF and leave every
// other job runnable.
//
// Before job-level isolation the parse error was returned as an ordinary error
// from CreateTaskForRunner: the runner's whole FetchTask failed with a 500, the
// bad job stayed Waiting, and the next poll picked it again. One repository with
// three malformed workflow files held every runner on the instance idle for 29
// hours — with a healthy backlog of 465 parseable jobs sitting behind it.
//
// The bad job is deliberately OLDER than the good one, so the scheduler is
// guaranteed to reach it first: this fails if the fix is only "the good job
// happens to be picked".
func TestCreateTaskForRunnerSkipsUnparsableJob(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	run := &ActionRun{
		Title:         "unparsable-test-run",
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         9904,
		TriggerUserID: 2,
		Ref:           "refs/heads/main",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), run))

	// Invalid YAML — the shape that jammed the instance: a mapping that never
	// closes, so the parser fails rather than producing a workflow.
	bad := &ActionRunJob{
		RunID:           run.ID,
		RepoID:          run.RepoID,
		OwnerID:         run.OwnerID,
		CommitSHA:       run.CommitSHA,
		Name:            "unparsable-job",
		Attempt:         1,
		JobID:           "unparsable-job",
		Status:          StatusWaiting,
		RunsOn:          []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: push\njobs:\n  unparsable-job:\n    runs-on: ubuntu-latest\n   steps:\n  -  - broken: [unclosed\n"),
	}
	require.NoError(t, db.Insert(t.Context(), bad))

	good := &ActionRunJob{
		RunID:           run.ID,
		RepoID:          run.RepoID,
		OwnerID:         run.OwnerID,
		CommitSHA:       run.CommitSHA,
		Name:            "healthy-job",
		Attempt:         1,
		JobID:           "healthy-job",
		Status:          StatusWaiting,
		RunsOn:          []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: push\njobs:\n  healthy-job:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"),
	}
	require.NoError(t, db.Insert(t.Context(), good))

	// The bad job must be the older candidate, so it is scanned first.
	_, err := db.GetEngine(t.Context()).ID(bad.ID).Cols("updated").
		Update(&ActionRunJob{Updated: good.Updated - 60})
	require.NoError(t, err)

	runner := &ActionRunner{
		UUID:        "unparsable-runner-uuid",
		Name:        "unparsable-runner",
		AgentLabels: []string{"ubuntu-latest"},
	}
	runner.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), runner))

	task, ok, err := CreateTaskForRunner(t.Context(), runner)
	require.NoError(t, err, "a malformed workflow must not fail the runner's request")
	require.True(t, ok, "the healthy job must still be handed out")
	require.NotNil(t, task)
	require.Equal(t, good.ID, task.JobID, "the scheduler must skip the bad job and claim the good one")

	// And the bad job must not sit in the queue being re-picked forever.
	reloaded, err := GetRunJobByRunAndID(t.Context(), run.ID, bad.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailure, reloaded.Status,
		"an unparsable job must fail on its own run, where its author can see it")
}

// newDroppedTask builds what a runner leaves behind when it accepts a task and
// is stopped before the first step: a running job holding a running task whose
// steps have not begun. drops seeds how many times the job has been handed back
// already.
func newDroppedTask(t *testing.T, name string, runIndex, drops int64) (*ActionTask, *ActionRunJob) {
	t.Helper()

	run := &ActionRun{
		Title:         name,
		RepoID:        1,
		OwnerID:       2,
		WorkflowID:    "test.yaml",
		Index:         runIndex,
		TriggerUserID: 2,
		Ref:           "refs/heads/main",
		CommitSHA:     "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Event:         "push",
		TriggerEvent:  "push",
		Status:        StatusRunning,
		Started:       timeutil.TimeStampNow(),
	}
	require.NoError(t, db.Insert(t.Context(), run))

	job := &ActionRunJob{
		RunID:     run.ID,
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
		Name:      name,
		Attempt:   1,
		JobID:     name,
		Status:    StatusRunning,
		Started:   timeutil.TimeStampNow(),
		Drops:     drops,
	}
	require.NoError(t, db.Insert(t.Context(), job))

	runner := &ActionRunner{
		UUID:                 "runner-" + name,
		Name:                 "runner-" + name,
		HasCancellingSupport: true,
	}
	require.NoError(t, db.Insert(t.Context(), runner))

	task := &ActionTask{
		JobID:     job.ID,
		Attempt:   1,
		RunnerID:  runner.ID,
		Status:    StatusRunning,
		Started:   timeutil.TimeStampNow(),
		RepoID:    run.RepoID,
		OwnerID:   run.OwnerID,
		CommitSHA: run.CommitSHA,
	}
	task.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), task))

	for i := int64(0); i < 2; i++ {
		require.NoError(t, db.Insert(t.Context(), &ActionTaskStep{
			Name:   "Run true",
			TaskID: task.ID,
			Index:  i,
			RepoID: task.RepoID,
			Status: StatusWaiting,
		}))
	}

	job.TaskID = task.ID
	_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	return task, job
}

// droppedState is what act_runner sends when it is stopped before the first
// step: the task failed, and every step is cancelled without ever having started.
func droppedState(taskID int64) *runnerv1.TaskState {
	return &runnerv1.TaskState{
		Id:        taskID,
		Result:    runnerv1.Result_RESULT_FAILURE,
		StoppedAt: timestamppb.Now(),
		Steps: []*runnerv1.StepState{
			{Id: 0, Result: runnerv1.Result_RESULT_CANCELLED},
			{Id: 1, Result: runnerv1.Result_RESULT_CANCELLED},
		},
	}
}

// A runner stopped between accepting a task and starting its first step reports
// the task failed with every step untouched. Nothing ran, so the job goes back
// to the queue for a healthy runner instead of ending red with nothing built.
func TestUpdateTaskByStateRequeuesDroppedTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "dropped-at-pickup", 9920, 0)

	updated, err := UpdateTaskByState(t.Context(), task.RunnerID, droppedState(task.ID))
	require.NoError(t, err)

	// The task keeps the runner's own account of itself; only the job moves.
	assert.Equal(t, StatusFailure, updated.Status)
	assert.Equal(t, StatusFailure, unittest.AssertExistsAndLoadBean(t, &ActionTask{ID: task.ID}).Status)

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusWaiting, after.Status, "a job that ran nothing belongs back in the queue")
	assert.Zero(t, after.TaskID, "the lost runner's task must be cleared for another runner to claim the job")
	assert.Zero(t, after.Started)
	assert.Zero(t, after.Stopped)
	assert.EqualValues(t, 1, after.Drops)
}

// The other half of the rule. A step that ran and exited non-zero is a real
// failure and stands, even when the whole task lives and dies inside a single
// report — which is the only chance such a step has to be seen at all.
func TestUpdateTaskByStateKeepsGenuineFailure(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "step-exited-nonzero", 9921, 0)

	state := droppedState(task.ID)
	state.Steps[0] = &runnerv1.StepState{
		Id:        0,
		Result:    runnerv1.Result_RESULT_FAILURE,
		StartedAt: timestamppb.Now(),
		StoppedAt: timestamppb.Now(),
	}

	updated, err := UpdateTaskByState(t.Context(), task.RunnerID, state)
	require.NoError(t, err)
	assert.Equal(t, StatusFailure, updated.Status)

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusFailure, after.Status, "a job whose step ran and failed must stay failed")
	assert.Equal(t, task.ID, after.TaskID)
	assert.Zero(t, after.Drops)
}

// A job nothing can get through must not circulate. At the cap it fails like any
// other, and the count stops moving.
func TestUpdateTaskByStateFailsAtDropCap(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "out-of-attempts", 9922, maxDrops)

	_, err := UpdateTaskByState(t.Context(), task.RunnerID, droppedState(task.ID))
	require.NoError(t, err)

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusFailure, after.Status, "past the cap a dropped job fails like any other")
	assert.EqualValues(t, maxDrops, after.Drops)
	assert.Equal(t, task.ID, after.TaskID)
}

// A cancel is a decision someone made. The runner reports failure for the
// cleanup phase of it, and that must not read as a job worth running again.
func TestUpdateTaskByStateDoesNotRequeueCancelledTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "user-cancelled", 9923, 0)
	require.NoError(t, StopTask(t.Context(), task.ID, StatusCancelling))

	_, err := UpdateTaskByState(t.Context(), task.RunnerID, droppedState(task.ID))
	require.NoError(t, err)

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusCancelled, after.Status, "a cancel must not be undone by a re-queue")
	assert.Zero(t, after.Drops)
}

// A job the sweep has already handed back is not this task's to settle any more.
// Calling twice must not double-count or drag the job out of the queue.
func TestRequeueDroppedTaskIsIdempotent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "requeued-twice", 9924, 0)

	requeued, err := RequeueDroppedTask(t.Context(), task)
	require.NoError(t, err)
	assert.True(t, requeued)

	requeued, err = RequeueDroppedTask(t.Context(), task)
	require.NoError(t, err)
	assert.False(t, requeued, "a job that no longer references this task is not its to move")

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusWaiting, after.Status)
	assert.EqualValues(t, 1, after.Drops)
}

// A task that has let go of its job must not settle it later. The sweep hands a
// dropped job back, a second runner claims it, and only then does the first
// runner's report land. Settling it there fails a job that is actively running,
// and the run's downstream jobs are skipped against a build that then succeeds.
func TestUpdateTaskByStateLeavesJobClaimedByAnotherTask(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	first, job := newDroppedTask(t, "let-go-of-its-job", 9925, 0)

	// The job has been re-queued and claimed again: it now runs someone else's task.
	second := &ActionTask{
		JobID:     job.ID,
		Attempt:   1,
		RunnerID:  first.RunnerID,
		Status:    StatusRunning,
		Started:   timeutil.TimeStampNow(),
		RepoID:    job.RepoID,
		OwnerID:   job.OwnerID,
		CommitSHA: job.CommitSHA,
	}
	second.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), second))
	job.TaskID = second.ID
	_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	// The first runner reports at last.
	_, err = UpdateTaskByState(t.Context(), first.RunnerID, droppedState(first.ID))
	require.NoError(t, err)

	after := unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID})
	assert.Equal(t, StatusRunning, after.Status, "a job running another task must not be settled by the task it let go")
	assert.Equal(t, second.ID, after.TaskID, "the claim of the runner actually doing the work must survive")
	assert.Zero(t, after.Drops, "a job this task no longer holds must not be charged a drop")
}

// Losing the guarded update says the job stopped being this task's to move. It
// does not say the job ran out of attempts, and the log must not say so either.
func TestRequeueDroppedTaskDoesNotClaimTheCapItDidNotReach(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	task, job := newDroppedTask(t, "lost-the-row", 9926, maxDrops-1)

	// The job moves on between this task letting go and the update landing.
	job.TaskID = 0
	_, err := UpdateRunJob(t.Context(), job, nil, "task_id")
	require.NoError(t, err)

	lc, cleanup := test.NewLogChecker(log.DEFAULT)
	lc.Filter("failing it", "re-queued after runner loss")
	defer cleanup()

	requeued, err := RequeueDroppedTask(t.Context(), task)
	require.NoError(t, err)
	assert.False(t, requeued)

	seen, _ := lc.Check(100 * time.Millisecond)
	assert.False(t, seen[0], "the cap was never reached, so nothing may report reaching it")
	assert.False(t, seen[1], "nothing was re-queued, so nothing may report re-queueing")
}
