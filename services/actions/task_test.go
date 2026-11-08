// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"testing"
	"time"

	actions_model "github.com/hanzoai/git/models/actions"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryPickTaskThrottled(t *testing.T) {
	sem := taskPickLimiter()

	// Saturate every assignment slot so the next attempt must be throttled.
	for range cap(sem) {
		sem <- struct{}{}
	}
	defer func() {
		for range cap(sem) {
			<-sem
		}
	}()

	// No DB access happens on the throttled path, so this is safe without fixtures.
	task, ok, throttled, err := TryPickTask(t.Context(), &actions_model.ActionRunner{})
	require.NoError(t, err)
	assert.Nil(t, task)
	assert.False(t, ok)
	assert.True(t, throttled)
}

// TestReleaseTaskForRunnerCleanup verifies the cleanup used by PickTask releases a claimed task through a
// fresh context. PickTask reaches this path when the request context is already canceled, and on
// PostgreSQL/MySQL a DB transaction on a canceled context fails immediately; reusing it would strand the
// claimed job in running state, so the cleanup must not use the request context.
func TestReleaseTaskForRunnerCleanup(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	run := &actions_model.ActionRun{
		Title: "cleanup-run", RepoID: 1, OwnerID: 2, WorkflowID: "test.yaml",
		TriggerUserID: 2, Ref: "refs/heads/main",
		CommitSHA: "c2d72f548424103f01ee1dc02889c1e2bff816b0", Event: "push", TriggerEvent: "push",
		Status: actions_model.StatusWaiting,
	}
	require.NoError(t, db.Insert(t.Context(), run))
	job := &actions_model.ActionRunJob{
		RunID: run.ID, RepoID: run.RepoID, OwnerID: run.OwnerID, CommitSHA: run.CommitSHA,
		Name: "cleanup-job", Attempt: 1, JobID: "cleanup-job", Status: actions_model.StatusWaiting,
		RunsOn:          []string{"ubuntu-latest"},
		WorkflowPayload: []byte("on: push\njobs:\n  cleanup-job:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"),
	}
	require.NoError(t, db.Insert(t.Context(), job))
	runner := &actions_model.ActionRunner{Name: "cleanup-runner", AgentLabels: []string{"ubuntu-latest"}}
	runner.GenerateAndFillToken()
	require.NoError(t, db.Insert(t.Context(), runner))

	task, ok, err := actions_model.CreateTaskForRunner(t.Context(), runner)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, actions_model.StatusRunning, unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID}).Status)

	// the cleanup helper uses its own context, so the claimed job is returned to the waiting queue
	releaseTaskForRunnerCleanup(task)
	released := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
	assert.Equal(t, actions_model.StatusWaiting, released.Status)
	assert.Zero(t, released.TaskID)
	unittest.AssertNotExistsBean(t, &actions_model.ActionTask{ID: task.ID})
}

func TestWakeRunners(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := db.Exec(t.Context(), "DELETE FROM action_run_job")
	require.NoError(t, err)

	global := func() int64 {
		v, err := actions_model.GetTasksVersionByScope(t.Context(), 0, 0)
		require.NoError(t, err)
		return v
	}
	// addJob makes job the only one in the table, last changed `age` ago.
	addJob := func(t *testing.T, job *actions_model.ActionRunJob, age time.Duration) {
		t.Helper()
		_, err := db.Exec(t.Context(), "DELETE FROM action_run_job")
		require.NoError(t, err)
		job.OwnerID, job.RepoID = 2, 1
		require.NoError(t, db.Insert(t.Context(), job))
		_, err = db.Exec(t.Context(), "UPDATE action_run_job SET updated = ? WHERE id = ?",
			timeutil.TimeStampNow().AddDuration(-age), job.ID)
		require.NoError(t, err)
	}
	waiting := func() *actions_model.ActionRunJob {
		return &actions_model.ActionRunJob{Status: actions_model.StatusWaiting, RunsOn: []string{"linux-amd64"}}
	}

	for name, tc := range map[string]struct {
		job  func() *actions_model.ActionRunJob
		age  time.Duration
		wake bool
	}{
		"waited over a minute":  {waiting, 2 * time.Minute, true},
		"waited under a minute": {waiting, 10 * time.Second, false},
		"already has a task": {func() *actions_model.ActionRunJob {
			j := waiting()
			j.TaskID = 7
			return j
		}, 2 * time.Minute, false},
		"not waiting": {func() *actions_model.ActionRunJob {
			j := waiting()
			j.Status = actions_model.StatusRunning
			return j
		}, 2 * time.Minute, false},
		"reusable workflow caller": {func() *actions_model.ActionRunJob {
			j := waiting()
			j.IsReusableCaller = true
			return j
		}, 2 * time.Minute, false},
		"no runs-on": {func() *actions_model.ActionRunJob {
			j := waiting()
			j.RunsOn = nil
			return j
		}, 2 * time.Minute, false},
		"empty runs-on": {func() *actions_model.ActionRunJob {
			j := waiting()
			j.RunsOn = []string{}
			return j
		}, 2 * time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			addJob(t, tc.job(), tc.age)
			before := global()
			require.NoError(t, WakeRunners(t.Context()))
			if tc.wake {
				assert.Equal(t, before+1, global())
			} else {
				assert.Equal(t, before, global())
			}
		})
	}

	t.Run("NoJobs", func(t *testing.T) {
		_, err := db.Exec(t.Context(), "DELETE FROM action_run_job")
		require.NoError(t, err)
		before := global()
		require.NoError(t, WakeRunners(t.Context()))
		assert.Equal(t, before, global())
	})
}

// A bump that fails after the transaction committed leaves idle runners on a version that never moves;
// the next WakeRunners tick moves it.
func TestWakeRunnersRepairsALostBump(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := db.Exec(t.Context(), "DELETE FROM action_run_job")
	require.NoError(t, err)

	job := &actions_model.ActionRunJob{OwnerID: 2, RepoID: 1, Status: actions_model.StatusWaiting, RunsOn: []string{"linux-amd64"}}
	require.NoError(t, db.Insert(t.Context(), job))
	_, err = db.Exec(t.Context(), "UPDATE action_run_job SET updated = ? WHERE id = ?",
		timeutil.TimeStampNow().AddDuration(-2*time.Minute), job.ID)
	require.NoError(t, err)

	global, err := actions_model.GetTasksVersionByScope(t.Context(), 0, 0)
	require.NoError(t, err)

	lc, cleanup := test.NewLogChecker(log.DEFAULT)
	lc.Filter("after 3 attempts")
	defer cleanup()

	// Take the table away while the transaction that created the work commits.
	_, err = db.Exec(t.Context(), "ALTER TABLE action_tasks_version RENAME TO action_tasks_version_off")
	require.NoError(t, err)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			_, err := db.Exec(t.Context(), "ALTER TABLE action_tasks_version_off RENAME TO action_tasks_version")
			require.NoError(t, err)
		}
	}
	defer restore()

	require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
		return actions_model.IncreaseTaskVersion(ctx, 2, 1)
	}))
	seen, _ := lc.Check(200 * time.Millisecond)
	assert.True(t, seen[0], "the lost bump must be logged")

	restore()
	after, err := actions_model.GetTasksVersionByScope(t.Context(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, global, after, "the failed bump left the version where it was")

	require.NoError(t, WakeRunners(t.Context()))
	after, err = actions_model.GetTasksVersionByScope(t.Context(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, global+1, after)
}
