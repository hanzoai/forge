// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"
	"time"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/assert"
)

func TestPushMirrorsIterate(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	now := timeutil.TimeStampNow()

	db.Insert(t.Context(), &repo_model.PushMirror{
		RepoID:         1,
		RemoteName:     "test-1",
		LastUpdateUnix: now,
		Interval:       1,
	})

	long, _ := time.ParseDuration("24h")
	db.Insert(t.Context(), &repo_model.PushMirror{
		RepoID:         1,
		RemoteName:     "test-2",
		LastUpdateUnix: now,
		Interval:       long,
	})

	db.Insert(t.Context(), &repo_model.PushMirror{
		RepoID:         1,
		RemoteName:     "test-3",
		LastUpdateUnix: now,
		Interval:       0,
	})

	var seen []string
	assert.NoError(t, repo_model.PushMirrorsIterate(t.Context(), 1, func(idx int, bean any) error {
		m, ok := bean.(*repo_model.PushMirror)
		assert.True(t, ok)
		assert.Equal(t, m.RemoteName, m.GetRemoteName())
		seen = append(seen, m.RemoteName)
		return nil
	}))
	assert.Equal(t, []string{"test-1"}, seen)
}

// A mirror is inserted without a last update: no sync has stamped one yet.
// The scheduler still has to select it, or it can never take its first run.
func TestPushMirrorsIterateNeverSynced(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	// Exactly what the API and web handlers write: last update left alone.
	assert.NoError(t, db.Insert(t.Context(), &repo_model.PushMirror{
		RepoID:     1,
		RemoteName: "never-synced",
		Interval:   time.Hour,
	}))

	var seen []string
	assert.NoError(t, repo_model.PushMirrorsIterate(t.Context(), 0, func(idx int, bean any) error {
		seen = append(seen, bean.(*repo_model.PushMirror).RemoteName)
		return nil
	}))
	assert.Equal(t, []string{"never-synced"}, seen)
}
