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

// versions reads the tasks version of each scope; a scope never bumped reads 0.
func versions(t *testing.T, scopes ...[2]int64) []int64 {
	t.Helper()
	out := make([]int64, len(scopes))
	for i, s := range scopes {
		v, err := actions_model.GetTasksVersionByScope(t.Context(), s[0], s[1])
		require.NoError(t, err)
		out[i] = v
	}
	return out
}

// addJob inserts a job in the given owner and repo that last changed `age` ago.
func addJob(t *testing.T, ownerID, repoID int64, job *actions_model.ActionRunJob, age time.Duration) {
	t.Helper()
	job.OwnerID, job.RepoID = ownerID, repoID
	require.NoError(t, db.Insert(t.Context(), job))
	_, err := db.Exec(t.Context(), "UPDATE action_run_job SET updated = ? WHERE id = ?",
		timeutil.TimeStampNow().AddDuration(-age), job.ID)
	require.NoError(t, err)
}

func clearJobs(t *testing.T) {
	t.Helper()
	_, err := db.Exec(t.Context(), "DELETE FROM action_run_job")
	require.NoError(t, err)
}

func waitingJob() *actions_model.ActionRunJob {
	return &actions_model.ActionRunJob{Status: actions_model.StatusWaiting, RunsOn: []string{"linux-amd64"}}
}

func TestWakeRunners(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// owner 2, repo 1: the job's own scopes, and the global one every runner of the estate may watch
	scopes := [][2]int64{{2, 0}, {0, 1}, {0, 0}}

	for name, tc := range map[string]struct {
		job  func() *actions_model.ActionRunJob
		age  time.Duration
		wake bool
	}{
		"waited over a minute":  {waitingJob, 2 * time.Minute, true},
		"waited under a minute": {waitingJob, 10 * time.Second, false},
		"already has a task": {func() *actions_model.ActionRunJob {
			j := waitingJob()
			j.TaskID = 7
			return j
		}, 2 * time.Minute, false},
		"not waiting": {func() *actions_model.ActionRunJob {
			j := waitingJob()
			j.Status = actions_model.StatusRunning
			return j
		}, 2 * time.Minute, false},
		"reusable workflow caller": {func() *actions_model.ActionRunJob {
			j := waitingJob()
			j.IsReusableCaller = true
			return j
		}, 2 * time.Minute, false},
		"no runs-on": {func() *actions_model.ActionRunJob {
			j := waitingJob()
			j.RunsOn = nil
			return j
		}, 2 * time.Minute, false},
		"empty runs-on": {func() *actions_model.ActionRunJob {
			j := waitingJob()
			j.RunsOn = []string{}
			return j
		}, 2 * time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			clearJobs(t)
			addJob(t, 2, 1, tc.job(), tc.age)
			before := versions(t, scopes...)
			require.NoError(t, WakeRunners(t.Context()))
			after := versions(t, scopes...)
			for i := range scopes {
				if tc.wake {
					assert.Equal(t, before[i]+1, after[i], "scope %v", scopes[i])
				} else {
					assert.Equal(t, before[i], after[i], "scope %v", scopes[i])
				}
			}
		})
	}

	t.Run("NoJobs", func(t *testing.T) {
		clearJobs(t)
		before := versions(t, scopes...)
		require.NoError(t, WakeRunners(t.Context()))
		assert.Equal(t, before, versions(t, scopes...))
	})

	// Several stalled jobs wake every scope they touch, and the shared global and owner rows only once.
	t.Run("EveryStalledScopeOnce", func(t *testing.T) {
		clearJobs(t)
		addJob(t, 2, 1, waitingJob(), 2*time.Minute)
		addJob(t, 2, 1, waitingJob(), 3*time.Minute) // same repo: one bump
		addJob(t, 2, 3, waitingJob(), 2*time.Minute) // same owner, another repo
		addJob(t, 5, 6, waitingJob(), 2*time.Minute) // another owner
		addJob(t, 7, 8, waitingJob(), 5*time.Second) // not stalled: untouched

		all := [][2]int64{{0, 0}, {2, 0}, {5, 0}, {7, 0}, {0, 1}, {0, 3}, {0, 6}, {0, 8}}
		before := versions(t, all...)
		require.NoError(t, WakeRunners(t.Context()))
		after := versions(t, all...)

		bumped := []bool{true, true, true, false, true, true, true, false}
		for i := range all {
			want := before[i]
			if bumped[i] {
				want++
			}
			assert.Equal(t, want, after[i], "scope %v", all[i])
		}
	})
}

// A bump that fails after the transaction committed leaves idle runners on a version that never moves;
// the next WakeRunners tick moves every scope of the job that was waiting.
func TestWakeRunnersRepairsALostBump(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	clearJobs(t)
	addJob(t, 2, 1, waitingJob(), 2*time.Minute)

	scopes := [][2]int64{{2, 0}, {0, 1}, {0, 0}}
	before := versions(t, scopes...)

	lc, cleanup := test.NewLogChecker(log.DEFAULT)
	lc.Filter("AfterCommit {ownerID:0 repoID:0}: ").StopMark("logs-done")
	defer cleanup()

	// Take the table away while the transaction that created the work commits.
	_, err := db.Exec(t.Context(), "ALTER TABLE action_tasks_version RENAME TO action_tasks_version_off")
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
		actions_model.IncreaseTaskVersion(ctx, 2, 1)
		return nil
	}))
	log.Error("logs-done") // events are read in order, so everything logged before this has been seen
	seen, _ := lc.Check(5 * time.Second)
	assert.True(t, seen[0], "the lost bump must be logged")

	restore()
	assert.Equal(t, before, versions(t, scopes...), "the failed bump left every version where it was")

	require.NoError(t, WakeRunners(t.Context()))
	after := versions(t, scopes...)
	for i := range scopes {
		assert.Equal(t, before[i]+1, after[i], "scope %v", scopes[i])
	}
}
