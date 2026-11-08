// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/queue"
	"github.com/hanzoai/git/modules/setting"
)

var (
	// mirrorQueue carries the scheduled sweep over every mirror.
	mirrorQueue *queue.WorkerPoolQueue[*SyncRequest]
	// eventQueue carries the syncs an event asked for, so they never wait behind
	// the sweep, which holds thousands of mirrors. Both run doMirrorSync, and a
	// repository's lock keeps a sync of one queue from overlapping the other's.
	eventQueue *queue.WorkerPoolQueue[*SyncRequest]
)

// SyncType type of sync request
type SyncType int

const (
	// PullMirrorType for pull mirrors
	PullMirrorType SyncType = iota
	// PushMirrorType for push mirrors
	PushMirrorType
	// InboundMirrorType for fast-forwarding a repository from the remote of its push mirror
	InboundMirrorType
)

// SyncRequest for the mirror queue
type SyncRequest struct {
	Type        SyncType
	ReferenceID int64 // RepoID for pull mirror, MirrorID for push and inbound mirror
}

func queueHandler(items ...*SyncRequest) []*SyncRequest {
	for _, req := range items {
		doMirrorSync(graceful.GetManager().ShutdownContext(), req)
	}
	return nil
}

// StartSyncMirrors starts a go routine for each mirror queue
func StartSyncMirrors() {
	if !setting.Mirror.Enabled {
		return
	}
	mirrorQueue = startQueue("mirror", queueHandler)
	eventQueue = startQueue("mirror_event", queueHandler)
}

func startQueue(name string, handler queue.HandlerFuncT[*SyncRequest]) *queue.WorkerPoolQueue[*SyncRequest] {
	q := queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), name, handler)
	if q == nil {
		log.Fatal("Unable to create %s queue", name)
	}
	go graceful.GetManager().RunWithCancel(q)
	return q
}

// AddPullMirrorToQueue adds repoID to mirror queue
func AddPullMirrorToQueue(repoID int64) {
	addMirrorToQueue(PushToQueue, PullMirrorType, repoID)
}

// AddPushMirrorToQueue adds the push mirror to the queue
func AddPushMirrorToQueue(mirrorID int64) {
	addMirrorToQueue(PushToQueue, PushMirrorType, mirrorID)
}

// AddPullMirrorToEventQueue adds repoID to the queue of syncs an event asked for
func AddPullMirrorToEventQueue(repoID int64) {
	addMirrorToQueue(PushToEventQueue, PullMirrorType, repoID)
}

// AddInboundMirrorToEventQueue adds the push mirror, whose remote is to be fast-forwarded in, to the queue of syncs an event asked for
func AddInboundMirrorToEventQueue(mirrorID int64) {
	addMirrorToQueue(PushToEventQueue, InboundMirrorType, mirrorID)
}

// addMirrorToQueue pushes with the given function on a goroutine, so a caller never waits on a queue.
func addMirrorToQueue(push func(SyncType, int64) error, syncType SyncType, referenceID int64) {
	if !setting.Mirror.Enabled {
		return
	}
	go func() {
		if err := push(syncType, referenceID); err != nil {
			log.Error("Unable to push sync request for to the queue for pull mirror repo[%d]. Error: %v", referenceID, err)
		}
	}()
}

// PushToQueue adds the sync request to the queue of the scheduled sweep
func PushToQueue(mirrorType SyncType, referenceID int64) error {
	return mirrorQueue.Push(&SyncRequest{
		Type:        mirrorType,
		ReferenceID: referenceID,
	})
}

// PushToEventQueue adds the sync request to the queue of syncs an event asked for
func PushToEventQueue(mirrorType SyncType, referenceID int64) error {
	return eventQueue.Push(&SyncRequest{
		Type:        mirrorType,
		ReferenceID: referenceID,
	})
}
