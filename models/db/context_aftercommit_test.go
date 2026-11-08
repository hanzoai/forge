// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAfterCommit(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	var ran []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			ran = append(ran, name)
			return nil
		}
	}
	reset := func() { ran = nil }

	t.Run("OutsideTransaction", func(t *testing.T) {
		reset()
		assert.False(t, db.AfterCommit(t.Context(), "a", record("a")))
		assert.Empty(t, ran)
	})

	t.Run("RunsAfterCommit", func(t *testing.T) {
		reset()
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			assert.True(t, db.AfterCommit(ctx, "a", record("a")))
			assert.Empty(t, ran, "must wait for the commit")
			return nil
		}))
		assert.Equal(t, []string{"a"}, ran)
	})

	t.Run("NotAfterRollback", func(t *testing.T) {
		reset()
		assert.Error(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			db.AfterCommit(ctx, "a", record("a"))
			return errors.New("fail")
		}))
		assert.Empty(t, ran)
	})

	t.Run("NestedRunsOnceAfterOutermostCommit", func(t *testing.T) {
		reset()
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			require.NoError(t, db.WithTx(ctx, func(ctx context.Context) error {
				db.AfterCommit(ctx, "inner", record("inner"))
				return nil
			}))
			assert.Empty(t, ran, "the inner transaction is not the outermost")
			db.AfterCommit(ctx, "outer", record("outer"))
			return nil
		}))
		assert.Equal(t, []string{"inner", "outer"}, ran)
	})

	t.Run("NestedRollbackRunsNothing", func(t *testing.T) {
		reset()
		// the outer function ignores the inner error, but the inner failure already rolled the transaction back,
		// and committing it afterwards reports no error
		_ = db.WithTx(t.Context(), func(ctx context.Context) error {
			db.AfterCommit(ctx, "outer", record("outer"))
			_ = db.WithTx(ctx, func(ctx context.Context) error {
				db.AfterCommit(ctx, "inner", record("inner"))
				return errors.New("fail")
			})
			return nil
		})
		assert.Empty(t, ran)
	})

	t.Run("SameKeyOnce", func(t *testing.T) {
		reset()
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			db.AfterCommit(ctx, "a", record("first"))
			db.AfterCommit(ctx, "a", record("second"))
			db.AfterCommit(ctx, "b", record("b"))
			return nil
		}))
		assert.Equal(t, []string{"first", "b"}, ran)
	})

	t.Run("TxContext", func(t *testing.T) {
		reset()
		ctx, committer, err := db.TxContext(t.Context())
		require.NoError(t, err)
		defer committer.Close()
		db.AfterCommit(ctx, "a", record("a"))
		assert.Empty(t, ran)
		require.NoError(t, committer.Commit())
		assert.Equal(t, []string{"a"}, ran)
	})

	t.Run("TxContextClosedWithoutCommit", func(t *testing.T) {
		reset()
		ctx, committer, err := db.TxContext(t.Context())
		require.NoError(t, err)
		db.AfterCommit(ctx, "a", record("a"))
		require.NoError(t, committer.Close())
		assert.Empty(t, ran)
	})

	t.Run("HooksShareOneNewTransaction", func(t *testing.T) {
		reset()
		var hookCtxs []context.Context
		var outer context.Context
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			outer = ctx
			for _, key := range []string{"a", "b"} {
				db.AfterCommit(ctx, key, func(ctx context.Context) error {
					hookCtxs = append(hookCtxs, ctx)
					assert.True(t, db.InTransaction(ctx))
					return nil
				})
			}
			return nil
		}))
		require.Len(t, hookCtxs, 2)
		assert.Equal(t, db.GetEngine(hookCtxs[0]), db.GetEngine(hookCtxs[1]))
		assert.NotEqual(t, db.GetEngine(outer), db.GetEngine(hookCtxs[0]))
	})

	t.Run("HookFailureDoesNotFailCommit", func(t *testing.T) {
		reset()
		require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
			db.AfterCommit(ctx, "a", func(context.Context) error { return errors.New("fail") })
			db.AfterCommit(ctx, "b", record("b"))
			return nil
		}))
		assert.Empty(t, ran, "a failing hook stops the ones after it")
	})

	t.Run("IgnoresCancellation", func(t *testing.T) {
		reset()
		ctx, cancel := context.WithCancel(t.Context())
		var hookErr error
		require.NoError(t, db.WithTx(ctx, func(ctx context.Context) error {
			db.AfterCommit(ctx, "a", func(ctx context.Context) error {
				hookErr = ctx.Err()
				return nil
			})
			cancel()
			return nil
		}))
		assert.NoError(t, hookErr)
	})
}
