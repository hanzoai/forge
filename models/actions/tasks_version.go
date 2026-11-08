// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"

	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/timeutil"
)

// ActionTasksVersion
// If both ownerID and repoID is zero, its scope is global.
// If ownerID is not zero and repoID is zero, its scope is org (there is no user-level runner currently).
// If ownerID is zero and repoID is not zero, its scope is repo.
type ActionTasksVersion struct {
	ID          int64 `xorm:"pk autoincr"`
	OwnerID     int64 `xorm:"UNIQUE(owner_repo)"`
	RepoID      int64 `xorm:"INDEX UNIQUE(owner_repo)"`
	Version     int64
	CreatedUnix timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func init() {
	db.RegisterModel(new(ActionTasksVersion))
}

func GetTasksVersionByScope(ctx context.Context, ownerID, repoID int64) (int64, error) {
	var tasksVersion ActionTasksVersion
	has, err := db.GetEngine(ctx).Where("owner_id = ? AND repo_id = ?", ownerID, repoID).Get(&tasksVersion)
	if err != nil {
		return 0, err
	} else if !has {
		return 0, nil
	}
	return tasksVersion.Version, err
}

func insertTasksVersion(ctx context.Context, ownerID, repoID int64) (*ActionTasksVersion, error) {
	tasksVersion := &ActionTasksVersion{
		OwnerID: ownerID,
		RepoID:  repoID,
		Version: 1,
	}
	if _, err := db.GetEngine(ctx).Insert(tasksVersion); err != nil {
		return nil, err
	}
	return tasksVersion, nil
}

func increaseTasksVersionByScope(ctx context.Context, ownerID, repoID int64) error {
	result, err := db.GetEngine(ctx).Exec("UPDATE action_tasks_version SET version = version + 1 WHERE owner_id = ? AND repo_id = ?", ownerID, repoID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		// if update sql does not affect any rows, the database may be broken,
		// so re-insert the row of version data here.
		if _, err := insertTasksVersion(ctx, ownerID, repoID); err != nil {
			return err
		}
	}

	return nil
}

// scope names one version row: {0, 0} is global, {owner, 0} an org, {0, repo} a repo.
type scope struct{ ownerID, repoID int64 }

// IncreaseTaskVersion bumps the repo, owner and global versions, in that order, so idle runners poll again.
// In a transaction the bumps wait for it to commit, and a rollback bumps nothing. Each scope is bumped once however
// often this is called, by a single autocommit statement of its own, so no version row is ever held across work.
// A bump announces work that has already committed, so a failure is logged and never returned to the caller;
// the wake_runners sweep repairs it.
func IncreaseTaskVersion(ctx context.Context, ownerID, repoID int64) {
	increaseTaskVersion(ctx, ownerID, repoID, increaseTasksVersionByScope)
}

func increaseTaskVersion(ctx context.Context, ownerID, repoID int64, increase func(ctx context.Context, ownerID, repoID int64) error) {
	scopes := make([]scope, 0, 3)
	if repoID > 0 {
		scopes = append(scopes, scope{repoID: repoID})
	}
	if ownerID > 0 {
		scopes = append(scopes, scope{ownerID: ownerID})
	}
	scopes = append(scopes, scope{}) // the global row is the busiest, so waiting on it delays the others least when it is last

	for _, s := range scopes {
		bump := func(ctx context.Context) error { return increase(ctx, s.ownerID, s.repoID) }
		if !db.AfterCommit(ctx, s, bump) { // not in a transaction: bump now
			if err := bump(ctx); err != nil {
				log.Error("IncreaseTaskVersion %+v: %v", s, err)
			}
		}
	}
}
