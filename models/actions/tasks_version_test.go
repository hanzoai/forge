// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"errors"
	"testing"
	"time"

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
		require.NoError(t, IncreaseTaskVersion(t.Context(), ownerID, repoID))
		assert.Equal(t, advanced(before, 1, 1, 1, 0), read(t, t.Context()))
	})

	t.Run("CommitBumpsOncePerScope", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, IncreaseTaskVersion(ctx, ownerID, repoID))
			require.NoError(t, IncreaseTaskVersion(ctx, ownerID, repoID))
			require.NoError(t, IncreaseTaskVersion(ctx, ownerID, otherRepoID))
			require.NoError(t, IncreaseTaskVersion(ctx, ownerID, repoID))
			assert.Equal(t, before, read(t, ctx), "unchanged until the transaction commits")
			return nil
		}))
		assert.Equal(t, advanced(before, 1, 1, 1, 1), read(t, t.Context()))
	})

	t.Run("NestedCommitBumpsOnce", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, db.WithTx(ctx, func(ctx context.Context) error {
				return IncreaseTaskVersion(ctx, ownerID, repoID)
			}))
			assert.Equal(t, before, read(t, ctx), "the inner transaction does not commit")
			return IncreaseTaskVersion(ctx, ownerID, repoID)
		}))
		assert.Equal(t, advanced(before, 1, 1, 1, 0), read(t, t.Context()))
	})

	t.Run("RollbackUnchanged", func(t *testing.T) {
		before := read(t, t.Context())
		assert.Error(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, IncreaseTaskVersion(ctx, ownerID, repoID))
			return errors.New("fail")
		}))
		assert.Equal(t, before, read(t, t.Context()))
	})

	t.Run("GlobalOnly", func(t *testing.T) {
		before := read(t, t.Context())
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			return IncreaseTaskVersion(ctx, 0, 0)
		}))
		assert.Equal(t, advanced(before, 1, 0, 0, 0), read(t, t.Context()))
	})
}

// flaky increases through the real statement, except that the first failures[s] attempts on scope s fail.
type flaky struct {
	failures map[scope]int
	calls    []scope
}

func (f *flaky) increase(ctx context.Context, ownerID, repoID int64) error {
	s := scope{ownerID, repoID}
	f.calls = append(f.calls, s)
	if f.failures[s] > 0 {
		f.failures[s]--
		return errors.New("database is busy")
	}
	return increaseTasksVersionByScope(ctx, ownerID, repoID)
}

func (f *flaky) callsOn(s scope) (n int) {
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}

func TestIncreaseTaskVersionRetry(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	const ownerID, repoID = 9101, 9102
	repo, owner, global := scope{repoID: repoID}, scope{ownerID: ownerID}, scope{}
	read := func(s scope) int64 {
		v, err := GetTasksVersionByScope(t.Context(), s.ownerID, s.repoID)
		require.NoError(t, err)
		return v
	}
	// commit runs one bump inside a transaction that commits.
	commit := func(f *flaky) {
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			return increaseTaskVersion(ctx, ownerID, repoID, f.increase)
		}))
	}

	t.Run("NarrowFirstGlobalLast", func(t *testing.T) {
		f := &flaky{}
		commit(f)
		assert.Equal(t, []scope{repo, owner, global}, f.calls)
	})

	t.Run("RetryThenSuccess", func(t *testing.T) {
		before := [3]int64{read(repo), read(owner), read(global)}
		f := &flaky{failures: map[scope]int{global: bumpAttempts - 1}}
		commit(f)
		assert.Equal(t, bumpAttempts, f.callsOn(global))
		assert.Equal(t, [3]int64{before[0] + 1, before[1] + 1, before[2] + 1}, [3]int64{read(repo), read(owner), read(global)})
	})

	t.Run("OneScopeFailingKeepsTheOthers", func(t *testing.T) {
		before := [3]int64{read(repo), read(owner), read(global)}
		lc, cleanup := test.NewLogChecker(log.DEFAULT)
		lc.Filter("after 3 attempts: database is busy")
		defer cleanup()

		f := &flaky{failures: map[scope]int{owner: bumpAttempts}}
		commit(f)

		assert.Equal(t, bumpAttempts, f.callsOn(owner), "gives up after the attempts")
		assert.Equal(t, 1, f.callsOn(repo))
		assert.Equal(t, 1, f.callsOn(global))
		assert.Equal(t, [3]int64{before[0] + 1, before[1], before[2] + 1}, [3]int64{read(repo), read(owner), read(global)})
		seen, _ := lc.Check(200 * time.Millisecond)
		assert.True(t, seen[0], "the final failure must be logged")
	})

	t.Run("OutsideTransactionReturnsTheFailure", func(t *testing.T) {
		before := [3]int64{read(repo), read(owner), read(global)}
		f := &flaky{failures: map[scope]int{repo: bumpAttempts}}
		err := increaseTaskVersion(t.Context(), ownerID, repoID, f.increase)
		require.ErrorContains(t, err, "after 3 attempts: database is busy")
		assert.Equal(t, [3]int64{before[0], before[1] + 1, before[2] + 1}, [3]int64{read(repo), read(owner), read(global)})
	})
}
