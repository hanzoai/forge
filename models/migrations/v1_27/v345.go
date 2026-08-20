// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"time"

	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/migrations/base"
	"github.com/hanzoai/git/modules/timeutil"
)

// Databases that gained last_update by ALTER carry it as nullable, and a push
// mirror is inserted without one: only a sync writes that column, so a mirror
// that has never synced holds NULL. The scheduler selects on
// last_update + interval <= now, which answers unknown for a NULL rather than
// true, so such a row is never selected and never gets the first sync that
// would give it a value.
//
// Zero says what a mirror that has never run means, in the terms the scheduler
// already reads: last synced at the epoch, therefore due now.
func MakePushMirrorLastUpdateNotNull(x db.EngineMigration) error {
	// RecreateTable builds the new table from this definition alone, so it is
	// the whole row, not just the column that changes.
	type PushMirror struct {
		ID            int64 `xorm:"pk autoincr"`
		RepoID        int64 `xorm:"INDEX"`
		RemoteName    string
		RemoteAddress string `xorm:"VARCHAR(2048)"`

		SyncOnCommit   bool `xorm:"NOT NULL DEFAULT true"`
		Interval       time.Duration
		CreatedUnix    timeutil.TimeStamp `xorm:"created"`
		LastUpdateUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL DEFAULT 0 last_update"`
		LastError      string             `xorm:"text"`
	}

	sess := x.NewSession()
	defer sess.Close()
	if err := sess.Begin(); err != nil {
		return err
	}

	// The rebuild carries each row over as COALESCE(column, its default), so the
	// rows that predate the constraint are filled by the same zero that now
	// stands behind every insert. One statement of it, in the column.
	if err := base.RecreateTable(sess, new(PushMirror)); err != nil {
		return err
	}

	return sess.Commit()
}
