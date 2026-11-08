// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sync an event asked for runs while every worker of the sweep queue is busy
// and more mirrors wait behind them, and the routing keeps the rest on the sweep.
// Push out on commit is such a sync; the settings page and the schedule are not.
func TestEventQueueDoesNotWaitForSweep(t *testing.T) {
	unittest.PrepareTestEnv(t)
	t.Cleanup(test.MockVariableValue(&setting.Mirror.Enabled, true))
	t.Cleanup(test.MockVariableValue(&mirrorQueue))
	t.Cleanup(test.MockVariableValue(&eventQueue))

	release := make(chan struct{})
	var busy atomic.Int32
	swept, heard := make(chan SyncRequest, 1000), make(chan SyncRequest, 10)

	mirrorQueue = startQueue("mirror", func(items ...*SyncRequest) []*SyncRequest {
		busy.Add(1)
		<-release
		for _, item := range items {
			swept <- *item
		}
		return nil
	})
	eventQueue = startQueue("mirror_event", func(items ...*SyncRequest) []*SyncRequest {
		for _, item := range items {
			heard <- *item
		}
		return nil
	})

	// Fill the sweep: more mirrors than its workers can take in their batches (at most 10 workers of 20).
	workers := int32(mirrorQueue.GetWorkerMaxNumber())
	for id := int64(1000); id < 1500; id++ {
		require.NoError(t, PushToQueue(PullMirrorType, id))
	}
	require.Eventually(t, func() bool { return busy.Load() >= workers }, 10*time.Second, 10*time.Millisecond, "the sweep never filled its workers")

	// What the notifier does on a push, and what a pull mirror does after its fetch.
	onCommit := &repo_model.PushMirror{RepoID: 1, RemoteName: "github", SyncOnCommit: true}
	require.NoError(t, db.Insert(t.Context(), onCommit))
	syncPushMirrorWithSyncOnCommit(t.Context(), 1)

	AddPullMirrorToEventQueue(101)
	AddInboundMirrorToEventQueue(102)
	AddPushMirrorToEventQueue(103)
	AddPullMirrorToQueue(104)
	AddPushMirrorToQueue(105)

	got := map[SyncRequest]bool{}
	for range 4 {
		select {
		case req := <-heard:
			got[req] = true
		case <-time.After(10 * time.Second):
			t.Fatal("an event sync waited behind the sweep")
		}
	}
	assert.Equal(t, map[SyncRequest]bool{
		{PullMirrorType, 101}:         true,
		{InboundMirrorType, 102}:      true,
		{PushMirrorType, 103}:         true,
		{PushMirrorType, onCommit.ID}: true,
	}, got)

	// Let the sweep drain: the plain adders land there, not on the event queue.
	close(release)
	want := map[SyncRequest]bool{{PullMirrorType, 104}: true, {PushMirrorType, 105}: true}
	require.Eventually(t, func() bool {
		for {
			select {
			case req := <-swept:
				delete(want, req)
			default:
				return len(want) == 0
			}
		}
	}, 60*time.Second, 10*time.Millisecond, "the plain adders did not reach the sweep queue")
	assert.Empty(t, heard, "the plain adders must not use the event queue")
}

// Each queue is tuned in its own section, so the event queue can be given workers
// without the sweep getting them.
func TestEventQueueHasItsOwnSettings(t *testing.T) {
	cfg, err := setting.NewConfigProviderFromData("[queue.mirror]\nMAX_WORKERS = 2\n[queue.mirror_event]\nMAX_WORKERS = 7\n")
	require.NoError(t, err)

	sweep, err := setting.GetQueueSettings(cfg, "mirror")
	require.NoError(t, err)
	event, err := setting.GetQueueSettings(cfg, "mirror_event")
	require.NoError(t, err)

	assert.Equal(t, 2, sweep.MaxWorkers)
	assert.Equal(t, 7, event.MaxWorkers)
}
