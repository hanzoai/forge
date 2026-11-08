// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"

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
