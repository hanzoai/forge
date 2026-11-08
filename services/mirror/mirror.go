// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"

	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/queue"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/timeutil"
)

// doMirrorSync causes this request to mirror itself
func doMirrorSync(ctx context.Context, req *SyncRequest) {
	if req.ReferenceID == 0 {
		log.Warn("Skipping mirror sync request, no mirror ID was specified")
		return
	}
	switch req.Type {
	case PushMirrorType:
		_ = SyncPushMirror(ctx, req.ReferenceID)
	case PullMirrorType:
		_ = SyncPullMirror(ctx, req.ReferenceID)
	case InboundMirrorType:
		_ = SyncInboundMirror(ctx, req.ReferenceID)
	default:
		log.Error("Unknown Request type in queue: %v for MirrorID[%d]", req.Type, req.ReferenceID)
	}
}

var errLimit = errors.New("reached limit")

// Update checks and updates mirror repositories.
func Update(ctx context.Context, pullLimit, pushLimit int) error {
	if !setting.Mirror.Enabled {
		log.Warn("Mirror feature disabled, but cron job enabled: skip update")
		return nil
	}
	log.Trace("Doing: Update")

	handler := func(bean any) error {
		var repo *repo_model.Repository
		var mirrorType SyncType
		var referenceID int64

		if m, ok := bean.(*repo_model.Mirror); ok {
			if m.GetRepository(ctx) == nil {
				log.Error("Disconnected mirror found: %d", m.ID)
				return nil
			}
			repo = m.Repo
			mirrorType = PullMirrorType
			referenceID = m.RepoID
		} else if m, ok := bean.(*repo_model.PushMirror); ok {
			if m.GetRepository(ctx) == nil {
				log.Error("Disconnected push-mirror found: %d", m.ID)
				return nil
			}
			repo = m.Repo
			mirrorType = PushMirrorType
			referenceID = m.ID
		} else {
			log.Error("Unknown bean: %v", bean)
			return nil
		}

		// Check we've not been cancelled
		select {
		case <-ctx.Done():
			return errors.New("aborted")
		default:
		}

		// Push to the Queue
		if err := PushToQueue(mirrorType, referenceID); err != nil {
			if err == queue.ErrAlreadyInQueue {
				if mirrorType == PushMirrorType {
					log.Trace("PushMirrors for %-v already queued for sync", repo)
				} else {
					log.Trace("PullMirrors for %-v already queued for sync", repo)
				}
				return nil
			}
			return err
		}
		return nil
	}

	pullMirrorsRequested := 0
	if pullLimit != 0 {
		if err := repo_model.MirrorsIterate(ctx, pullLimit, func(_ int, bean any) error {
			if err := handler(bean); err != nil {
				return err
			}
			pullMirrorsRequested++
			return nil
		}); err != nil && err != errLimit {
			log.Error("MirrorsIterate: %v", err)
			return err
		}
	}

	pushMirrorsRequested := 0
	if pushLimit != 0 {
		if err := repo_model.PushMirrorsIterate(ctx, pushLimit, func(idx int, bean any) error {
			if err := handler(bean); err != nil {
				return err
			}
			pushMirrorsRequested++
			return nil
		}); err != nil && err != errLimit {
			log.Error("PushMirrorsIterate: %v", err)
			return err
		}
	}
	inboundRequested := 0
	if pushLimit != 0 && setting.Mirror.SyncUser != "" {
		n, err := updateInbound(ctx, pushLimit)
		if err != nil {
			return err
		}
		inboundRequested = n
	}
	log.Trace("Finished: Update: %d pull mirrors, %d push mirrors and %d inbound syncs queued", pullMirrorsRequested, pushMirrorsRequested, inboundRequested)
	return nil
}

// updateInbound queues an inbound sync for each push mirror of a regular
// repository to GitHub that has had none for a while, so a missed event heals.
// It runs on its own clock: a busy repository pushes out all the time, and
// would never come due on the push out's.
func updateInbound(ctx context.Context, limit int) (int, error) {
	var due []*repo_model.PushMirror
	if err := repo_model.InboundPushMirrorsIterate(ctx, limit, setting.Mirror.DefaultInterval, func(_ int, bean any) error {
		due = append(due, bean.(*repo_model.PushMirror))
		return nil
	}); err != nil {
		log.Error("InboundPushMirrorsIterate: %v", err)
		return 0, err
	}

	queued := 0
	for _, m := range due {
		select {
		case <-ctx.Done():
			return queued, errors.New("aborted")
		default:
		}
		repo := m.GetRepository(ctx)
		if repo == nil {
			log.Error("Disconnected push-mirror found: %d", m.ID)
			continue
		}

		// Either way it goes to the back of the line, or the same ones would be picked every time.
		m.LastInboundUnix = timeutil.TimeStampNow()
		if err := repo_model.UpdatePushMirrorLastInbound(ctx, m); err != nil {
			return queued, err
		}

		if !isRegular(ctx, repo) {
			continue
		}
		if !IsGitHubRepo(m.RemoteAddress, repo.OwnerName, repo.Name) {
			if path, ok := githubRepoPath(m.RemoteAddress); ok {
				log.Warn("Push mirror %d of %s mirrors to github.com/%s, so inbound sync is off for it", m.ID, repo.FullName(), path)
			}
			continue
		}
		if err := PushToQueue(InboundMirrorType, m.ID); err != nil && !errors.Is(err, queue.ErrAlreadyInQueue) {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// InitSyncMirrors initializes a go routine to sync the mirrors
func InitSyncMirrors() {
	StartSyncMirrors()
}
