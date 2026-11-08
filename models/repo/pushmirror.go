// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"time"

	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/optional"
	"github.com/hanzoai/git/modules/timeutil"
	"github.com/hanzoai/git/modules/util"

	"github.com/hanzoai/builder"
)

// PushMirror represents mirror information of a repository.
type PushMirror struct {
	ID            int64       `xorm:"pk autoincr"`
	RepoID        int64       `xorm:"INDEX"`
	Repo          *Repository `xorm:"-"`
	RemoteName    string
	RemoteAddress string `xorm:"VARCHAR(2048)"`

	SyncOnCommit bool `xorm:"NOT NULL DEFAULT true"`
	Interval     time.Duration
	CreatedUnix  timeutil.TimeStamp `xorm:"created"`
	// Zero means the epoch, which is due now: a mirror nothing has synced yet
	// is owed its first run. The scheduler compares this to the wall clock, and
	// no comparison against NULL is ever true, so the column must never hold one.
	LastUpdateUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL DEFAULT 0 last_update"`
	LastError      string             `xorm:"text"`

	// When the inbound sync last looked at the remote, or was last scheduled to.
	// It has its own clock because a busy repository pushes out constantly,
	// which keeps last_update fresh and would keep its inbound sync from ever being due.
	LastInboundUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL DEFAULT 0"`
}

type PushMirrorOptions struct {
	db.ListOptions
	ID         int64
	RepoID     int64
	RemoteName string
}

func (opts PushMirrorOptions) ToConds() builder.Cond {
	cond := builder.NewCond()
	if opts.RepoID > 0 {
		cond = cond.And(builder.Eq{"repo_id": opts.RepoID})
	}
	if opts.RemoteName != "" {
		cond = cond.And(builder.Eq{"remote_name": opts.RemoteName})
	}
	if opts.ID > 0 {
		cond = cond.And(builder.Eq{"id": opts.ID})
	}
	return cond
}

func init() {
	db.RegisterModel(new(PushMirror))
}

// GetRepository returns the path of the repository.
func (m *PushMirror) GetRepository(ctx context.Context) *Repository {
	if m.Repo != nil {
		return m.Repo
	}
	var err error
	m.Repo, err = GetRepositoryByID(ctx, m.RepoID)
	if err != nil {
		log.Error("getRepositoryByID[%d]: %v", m.ID, err)
	}
	return m.Repo
}

// GetRemoteName returns the name of the remote.
func (m *PushMirror) GetRemoteName() string {
	return m.RemoteName
}

// UpdatePushMirror updates the push-mirror. It leaves the inbound clock alone: a
// push out holds a copy of the row for as long as it runs, and writing that copy
// back would undo what an inbound sync recorded in the meantime.
func UpdatePushMirror(ctx context.Context, m *PushMirror) error {
	_, err := db.GetEngine(ctx).ID(m.ID).Cols("remote_name", "remote_address", "sync_on_commit", "interval", "last_update", "last_error").Update(m)
	return err
}

// UpdatePushMirrorLastInbound records when the inbound sync last ran for the push-mirror
func UpdatePushMirrorLastInbound(ctx context.Context, m *PushMirror) error {
	_, err := db.GetEngine(ctx).ID(m.ID).Cols("last_inbound_unix").Update(m)
	return err
}

// UpdatePushMirrorInterval updates the push-mirror
func UpdatePushMirrorInterval(ctx context.Context, m *PushMirror) error {
	_, err := db.GetEngine(ctx).ID(m.ID).Cols("interval").Update(m)
	return err
}

func DeletePushMirrors(ctx context.Context, opts PushMirrorOptions) error {
	if opts.RepoID > 0 {
		_, err := db.Delete[PushMirror](ctx, opts)
		return err
	}
	return util.NewInvalidArgumentErrorf("repoID required and must be set")
}

type findPushMirrorOptions struct {
	db.ListOptions
	RepoID       int64
	SyncOnCommit optional.Option[bool]
}

func (opts findPushMirrorOptions) ToConds() builder.Cond {
	cond := builder.NewCond()
	if opts.RepoID > 0 {
		cond = cond.And(builder.Eq{"repo_id": opts.RepoID})
	}
	if opts.SyncOnCommit.Has() {
		cond = cond.And(builder.Eq{"sync_on_commit": opts.SyncOnCommit.Value()})
	}
	return cond
}

// GetPushMirrorsByRepoID returns push-mirror information of a repository.
func GetPushMirrorsByRepoID(ctx context.Context, repoID int64, listOptions db.ListOptions) ([]*PushMirror, int64, error) {
	return db.FindAndCount[PushMirror](ctx, findPushMirrorOptions{
		ListOptions: listOptions,
		RepoID:      repoID,
	})
}

func GetPushMirrorByIDAndRepoID(ctx context.Context, id, repoID int64) (*PushMirror, bool, error) {
	var pushMirror PushMirror
	has, err := db.GetEngine(ctx).Where("id = ?", id).And("repo_id = ?", repoID).Get(&pushMirror)
	if !has || err != nil {
		return nil, has, err
	}
	return &pushMirror, true, nil
}

// GetPushMirrorsSyncedOnCommit returns push-mirrors for this repo that should be updated by new commits
func GetPushMirrorsSyncedOnCommit(ctx context.Context, repoID int64) ([]*PushMirror, error) {
	return db.Find[PushMirror](ctx, findPushMirrorOptions{
		RepoID:       repoID,
		SyncOnCommit: optional.Some(true),
	})
}

// PushMirrorsIterate iterates all push-mirror repositories.
func PushMirrorsIterate(ctx context.Context, limit int, f func(idx int, bean any) error) error {
	sess := db.GetEngine(ctx).
		Table("push_mirror").
		Join("INNER", "`repository`", "`repository`.id = `push_mirror`.repo_id").
		Where("`push_mirror`.last_update + (`push_mirror`.`interval` / ?) <= ?", time.Second, time.Now().Unix()).
		And("`push_mirror`.`interval` != 0").
		And("`repository`.is_archived = ?", false).
		OrderBy("last_update ASC")
	if limit > 0 {
		sess = sess.Limit(limit)
	}
	return sess.Iterate(new(PushMirror), f)
}

// InboundPushMirrorsIterate iterates the push-mirrors that may point at GitHub, on
// repositories that are neither archived nor pull mirrors, whose inbound sync was
// last run more than period ago, the longest idle first.
func InboundPushMirrorsIterate(ctx context.Context, limit int, period time.Duration, f func(idx int, bean any) error) error {
	sess := db.GetEngine(ctx).
		Table("push_mirror").
		Join("INNER", "`repository`", "`repository`.id = `push_mirror`.repo_id").
		Where("LOWER(`push_mirror`.remote_address) LIKE ?", "%github.com%").
		And("`push_mirror`.last_inbound_unix + ? <= ?", int64(period/time.Second), time.Now().Unix()).
		And("`repository`.is_archived = ?", false).
		And("`repository`.is_mirror = ?", false).
		OrderBy("`push_mirror`.last_inbound_unix ASC")
	if limit > 0 {
		sess = sess.Limit(limit)
	}
	return sess.Iterate(new(PushMirror), f)
}
