// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"testing"
	"time"

	"github.com/hanzoai/git/models/migrations/migrationtest"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/stretchr/testify/require"
)

// A push mirror as it was before the column.
type pushMirrorBeforeInbound struct {
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

func (pushMirrorBeforeInbound) TableName() string {
	return "push_mirror"
}

func Test_AddLastInboundToPushMirror(t *testing.T) {
	x, deferrable := migrationtest.PrepareTestEnv(t, 0, new(pushMirrorBeforeInbound))
	defer deferrable()

	_, err := x.Insert(&pushMirrorBeforeInbound{RepoID: 1, RemoteName: "existing"})
	require.NoError(t, err)

	require.NoError(t, AddLastInboundToPushMirror(x))
	require.NoError(t, AddLastInboundToPushMirror(x)) // idempotent

	// The mirror that predates the column has never had an inbound sync: due now, not unknown.
	due, err := x.Table("push_mirror").Where("`last_inbound_unix` = 0").Count()
	require.NoError(t, err)
	require.EqualValues(t, 1, due)

	// And the column will not take a NULL.
	_, err = x.Exec("UPDATE `push_mirror` SET `last_inbound_unix` = NULL")
	require.Error(t, err)
}
