// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"errors"
	"testing"
	"time"

	runner_module "github.com/hanzoai/forge/modules/actions/runner"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIncreaseTaskVersion(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const ownerID, repoID, otherRepoID = 9001, 9002, 9003
	scopes := []scope{{}, {ownerID: ownerID}, {repoID: repoID}, {repoID: otherRepoID}}

	read := func(t *testing.T, ctx context.Context) []int64 {
		versions := make([]int64, len(scopes))
		for i, s := range scopes {
			v, err := GetTasksVersionByScope(ctx, s.ownerID, s.repoID)
			require.NoError(t, err)
			versions[i] = v
		}
		return versions
	}
	// advanced is the versions after `by` bumps of each scope, given the versions before.
	advanced := func(before []int64, by ...int64) []int64 {
		after := make([]int64, len(before))
		for i := range before {
			after[i] = before[i] + by[i]
		}
		return after
	}

	t.Run("OutsideTransactionImmediate", func(t *testing.T) {
		before := read(t, t.Context())
		IncreaseTaskVersion(t.Context(), ownerID, repoID)
		assert.Equal(t, advanced(before, 1, 1, 1, 0), read(t, t.Context()))
	})

	t.Run("CommitBumpsOncePerScope", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			IncreaseTaskVersion(ctx, ownerID, repoID)
			IncreaseTaskVersion(ctx, ownerID, repoID)
			IncreaseTaskVersion(ctx, ownerID, otherRepoID)
			IncreaseTaskVersion(ctx, ownerID, repoID)
			assert.Equal(t, before, read(t, ctx), "unchanged until the transaction commits")
			return nil
		}))
		assert.Equal(t, advanced(before, 1, 1, 1, 1), read(t, t.Context()))
	})

	t.Run("NestedCommitBumpsOnce", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, db.WithTx(ctx, func(ctx context.Context) error {
				IncreaseTaskVersion(ctx, ownerID, repoID)
				return nil
			}))
			assert.Equal(t, before, read(t, ctx), "the inner transaction does not commit")
			IncreaseTaskVersion(ctx, ownerID, repoID)
			return nil
		}))
		assert.Equal(t, advanced(before, 1, 1, 1, 0), read(t, t.Context()))
	})

	t.Run("RollbackUnchanged", func(t *testing.T) {
		before := read(t, t.Context())
		assert.Error(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			IncreaseTaskVersion(ctx, ownerID, repoID)
			return errors.New("fail")
		}))
		assert.Equal(t, before, read(t, t.Context()))
	})

	t.Run("GlobalOnly", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			IncreaseTaskVersion(ctx, 0, 0)
			return nil
		}))
		assert.Equal(t, advanced(before, 1, 0, 0, 0), read(t, t.Context()))
	})
}

// flaky increases through the real statement, except that scopes in failing always fail.
type flaky struct {
	failing map[scope]bool
	calls   []scope
}

func (f *flaky) increase(ctx context.Context, ownerID, repoID int64) error {
	s := scope{ownerID, repoID}
	f.calls = append(f.calls, s)
	if f.failing[s] {
		return errors.New("database is busy")
	}
	return increaseTasksVersionByScope(ctx, ownerID, repoID)
}

func TestIncreaseTaskVersionFailure(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const ownerID, repoID = 9101, 9102
	repo, owner, global := scope{repoID: repoID}, scope{ownerID: ownerID}, scope{}
	read := func() [3]int64 {
		var v [3]int64
		for i, s := range []scope{repo, owner, global} {
			var err error
			v[i], err = GetTasksVersionByScope(t.Context(), s.ownerID, s.repoID)
			require.NoError(t, err)
		}
		return v
	}

	t.Run("NarrowFirstGlobalLast", func(t *testing.T) {
		f := &flaky{}
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			increaseTaskVersion(ctx, ownerID, repoID, f.increase)
			return nil
		}))
		assert.Equal(t, []scope{repo, owner, global}, f.calls)
	})

	t.Run("OneScopeFailingKeepsTheOthers", func(t *testing.T) {
		before := read()
		lc, cleanup := test.NewLogChecker(log.DEFAULT)
		lc.Filter("AfterCommit {ownerID:9101 repoID:0}: database is busy").StopMark("logs-done")
		defer cleanup()

		f := &flaky{failing: map[scope]bool{owner: true}}
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			increaseTaskVersion(ctx, ownerID, repoID, f.increase)
			return nil
		}))

		assert.Equal(t, []scope{repo, owner, global}, f.calls, "each scope is tried once, and the failure stops none")
		assert.Equal(t, [3]int64{before[0] + 1, before[1], before[2] + 1}, read())
		log.Error("logs-done") // events are read in order, so everything logged before this has been seen
		seen, _ := lc.Check(5 * time.Second)
		assert.True(t, seen[0], "the failure must be logged")
	})

	t.Run("OutsideTransactionLogsAndDoesNotReturn", func(t *testing.T) {
		before := read()
		lc, cleanup := test.NewLogChecker(log.DEFAULT)
		lc.Filter("IncreaseTaskVersion {ownerID:0 repoID:9102}: database is busy").StopMark("logs-done")
		defer cleanup()

		f := &flaky{failing: map[scope]bool{repo: true}}
		increaseTaskVersion(t.Context(), ownerID, repoID, f.increase)

		assert.Equal(t, [3]int64{before[0], before[1] + 1, before[2] + 1}, read())
		log.Error("logs-done") // events are read in order, so everything logged before this has been seen
		seen, _ := lc.Check(5 * time.Second)
		assert.True(t, seen[0], "the failure must be logged")
	})
}

// withoutTasksVersion takes the version table away, so every bump fails, until the returned function puts it back.
func withoutTasksVersion(t *testing.T) (restore func()) {
	t.Helper()
	_, err := db.Exec(t.Context(), "ALTER TABLE action_tasks_version RENAME TO action_tasks_version_off")
	require.NoError(t, err)
	return func() {
		_, err := db.Exec(t.Context(), "ALTER TABLE action_tasks_version_off RENAME TO action_tasks_version")
		require.NoError(t, err)
	}
}

// UpdateTaskByState settles a finished task outside any transaction, and a runner that gets an error back reports
// again into "state is final", which skips the rest of the job's settlement. A bump that fails must not be that error.
func TestUpdateTaskByStateSurvivesAFailedBump(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer withoutTasksVersion(t)()

	task, job := newDroppedTask(t, "bump-fails-finished", 9930, 0)
	// Waiting work remains in the repo, which is what makes a finished job bump the version.
	require.NoError(t, db.Insert(t.Context(), &ActionRunJob{
		RunID: -1, RepoID: job.RepoID, OwnerID: job.OwnerID, Name: "waiting", JobID: "waiting",
		Status: StatusWaiting, RunsOn: []string{"linux-amd64"},
	}))

	now := time.Now().UnixNano()
	_, err := UpdateTaskByState(t.Context(), task.RunnerID, runner_module.State{
		ID:      task.ID,
		Result:  runner_module.Success,
		Stopped: now,
		Steps: []runner_module.Step{
			{ID: 0, Result: runner_module.Success, Started: now, Stopped: now},
			{ID: 1, Result: runner_module.Success, Started: now, Stopped: now},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, StatusSuccess, unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID}).Status)
	assert.Equal(t, StatusSuccess, unittest.AssertExistsAndLoadBean(t, &ActionRun{ID: job.RunID}).Status,
		"the run must still be settled from its jobs")
}

// Same, for the transaction that puts a dropped job back in the queue: its bump follows the commit.
func TestUpdateTaskByStateRequeuesThroughAFailedBump(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer withoutTasksVersion(t)()

	task, job := newDroppedTask(t, "bump-fails-requeued", 9931, 0)

	_, err := UpdateTaskByState(t.Context(), task.RunnerID, droppedState(task.ID))
	require.NoError(t, err)

	assert.Equal(t, StatusWaiting, unittest.AssertExistsAndLoadBean(t, &ActionRunJob{ID: job.ID}).Status)
}
