// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/queue"
	"github.com/hanzoai/git/modules/setting"
)

var mirrorQueue *queue.WorkerPoolQueue[*SyncRequest]

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

// StartSyncMirrors starts a go routine to sync the mirrors
func StartSyncMirrors() {
	if !setting.Mirror.Enabled {
		return
	}
	mirrorQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "mirror", queueHandler)
	if mirrorQueue == nil {
		log.Fatal("Unable to create mirror queue")
	}
	go graceful.GetManager().RunWithCancel(mirrorQueue)
}

// AddPullMirrorToQueue adds repoID to mirror queue
func AddPullMirrorToQueue(repoID int64) {
	addMirrorToQueue(PullMirrorType, repoID)
}

// AddPushMirrorToQueue adds the push mirror to the queue
func AddPushMirrorToQueue(mirrorID int64) {
	addMirrorToQueue(PushMirrorType, mirrorID)
}

// AddInboundMirrorToQueue adds the push mirror, whose remote is to be fast-forwarded in, to the queue
func AddInboundMirrorToQueue(mirrorID int64) {
	addMirrorToQueue(InboundMirrorType, mirrorID)
}

func addMirrorToQueue(syncType SyncType, referenceID int64) {
	if !setting.Mirror.Enabled {
		return
	}
	go func() {
		if err := PushToQueue(syncType, referenceID); err != nil {
			log.Error("Unable to push sync request for to the queue for pull mirror repo[%d]. Error: %v", referenceID, err)
		}
	}()
}

// PushToQueue adds the sync request to the queue
func PushToQueue(mirrorType SyncType, referenceID int64) error {
	return mirrorQueue.Push(&SyncRequest{
		Type:        mirrorType,
		ReferenceID: referenceID,
	})
}
