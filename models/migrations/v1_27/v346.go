// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/modules/timeutil"

	"github.com/hanzoai/xorm"
)

// AddLastInboundToPushMirror adds the time the inbound sync last looked at a push
// mirror's remote. It is its own column because last_update moves with every
// push out, and a busy repository's inbound sync would never come due on it.
// Zero, the epoch, says a mirror that has never been looked at is due now.
func AddLastInboundToPushMirror(x db.EngineMigration) error {
	type PushMirror struct {
		LastInboundUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL DEFAULT 0"`
	}

	_, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreDropIndices: true,
		IgnoreConstrains:  true,
	}, new(PushMirror))
	return err
}
