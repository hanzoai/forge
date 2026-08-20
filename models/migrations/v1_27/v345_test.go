// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"testing"
	"time"

	"github.com/hanzoai/git/models/migrations/migrationtest"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/require"
)

// last_update as older databases hold it. A pointer is how xorm writes a
// column that accepts NULL.
type pushMirrorNullableLastUpdate struct {
	ID            int64 `xorm:"pk autoincr"`
	RepoID        int64 `xorm:"INDEX"`
	RemoteName    string
	RemoteAddress string `xorm:"VARCHAR(2048)"`

	SyncOnCommit   bool `xorm:"NOT NULL DEFAULT true"`
	Interval       time.Duration
	CreatedUnix    timeutil.TimeStamp  `xorm:"created"`
	LastUpdateUnix *timeutil.TimeStamp `xorm:"INDEX last_update"`
	LastError      string              `xorm:"text"`
}

func (pushMirrorNullableLastUpdate) TableName() string {
	return "push_mirror"
}

func Test_MakePushMirrorLastUpdateNotNull(t *testing.T) {
	x, deferrable := migrationtest.PrepareTestEnv(t, 0, new(pushMirrorNullableLastUpdate))
	defer deferrable()

	// A mirror that was created and never synced: nothing wrote a last update.
	_, err := x.Insert(&pushMirrorNullableLastUpdate{
		RepoID:     1,
		RemoteName: "never-synced",
		Interval:   time.Hour,
	})
	require.NoError(t, err)

	dormant, err := x.Table("push_mirror").Where("`last_update` IS NULL").Count()
	require.NoError(t, err)
	require.EqualValues(t, 1, dormant)

	require.NoError(t, MakePushMirrorLastUpdateNotNull(x))
	require.NoError(t, MakePushMirrorLastUpdateNotNull(x)) // idempotent

	// It now reads as due rather than as unknown.
	due, err := x.Table("push_mirror").Where("`last_update` = 0").Count()
	require.NoError(t, err)
	require.EqualValues(t, 1, due)

	// And the column will not take a NULL again.
	_, err = x.Exec("UPDATE `push_mirror` SET `last_update` = NULL")
	require.Error(t, err)
}
