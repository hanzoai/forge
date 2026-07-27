// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
)

func runWorkerPoolQueue[T any](q *WorkerPoolQueue[T]) func() {
	go q.Run()
	return func() {
		q.ShutdownWait(1 * time.Second)
	}
}

func TestWorkerPoolQueueUnhandled(t *testing.T) {
	oldUnhandledItemRequeueDuration := unhandledItemRequeueDuration.Load()
	unhandledItemRequeueDuration.Store(0)
	defer unhandledItemRequeueDuration.Store(oldUnhandledItemRequeueDuration)

	mu := sync.Mutex{}

	test := func(t *testing.T, queueSetting setting.QueueSettings) {
		queueSetting.Length = 100
		queueSetting.Type = "zapdb"
		queueSetting.Datadir = t.TempDir() + "/test-queue"
		m := map[int]int{}

		// odds are handled once, evens are handled twice
		handler := func(items ...int) (unhandled []int) {
			testRecorder.Record("handle:%v", items)
			for _, item := range items {
				mu.Lock()
				if item%2 == 0 && m[item] == 0 {
					unhandled = append(unhandled, item)
				}
				m[item]++
				mu.Unlock()
			}
			return unhandled
		}

		q, _ := newWorkerPoolQueueForTest("test-workpoolqueue", queueSetting, handler, false)
		stop := runWorkerPoolQueue(q)
		for i := 0; i < queueSetting.Length; i++ {
			testRecorder.Record("push:%v", i)
			assert.NoError(t, q.Push(i))
		}
		assert.NoError(t, q.FlushWithContext(t.Context(), 0))
		stop()

		ok := true
		for i := 0; i < queueSetting.Length; i++ {
			if i%2 == 0 {
				ok = ok && assert.Equal(t, 2, m[i], "test %s: item %d", t.Name(), i)
			} else {
				ok = ok && assert.Equal(t, 1, m[i], "test %s: item %d", t.Name(), i)
			}
		}
		if !ok {
			t.Logf("m: %v", m)
			t.Logf("records: %v", testRecorder.Records())
		}
		testRecorder.Reset()
	}

	runCount := 2 // we can run these tests even hundreds times to see its stability
	t.Run("1/1", func(t *testing.T) {
		for range runCount {
			test(t, setting.QueueSettings{BatchLength: 1, MaxWorkers: 1})
		}
	})
	t.Run("3/1", func(t *testing.T) {
		for range runCount {
			test(t, setting.QueueSettings{BatchLength: 3, MaxWorkers: 1})
		}
	})
	t.Run("4/5", func(t *testing.T) {
		for range runCount {
			test(t, setting.QueueSettings{BatchLength: 4, MaxWorkers: 5})
		}
	})
}

func TestWorkerPoolQueuePersistence(t *testing.T) {
	runCount := 2 // we can run these tests even hundreds times to see its stability
	t.Run("1/1", func(t *testing.T) {
		for range runCount {
			testWorkerPoolQueuePersistence(t, setting.QueueSettings{BatchLength: 1, MaxWorkers: 1, Length: 100})
		}
	})
	t.Run("3/1", func(t *testing.T) {
		for range runCount {
			testWorkerPoolQueuePersistence(t, setting.QueueSettings{BatchLength: 3, MaxWorkers: 1, Length: 100})
		}
	})
	t.Run("4/5", func(t *testing.T) {
		for range runCount {
			testWorkerPoolQueuePersistence(t, setting.QueueSettings{BatchLength: 4, MaxWorkers: 5, Length: 100})
		}
	})
}

func testWorkerPoolQueuePersistence(t *testing.T, queueSetting setting.QueueSettings) {
	testCount := queueSetting.Length
	queueSetting.Type = "zapdb"
	queueSetting.Datadir = t.TempDir() + "/test-queue"

	mu := sync.Mutex{}

	var tasksQ1, tasksQ2 []string
	q1 := func() {
		startWhenAllReady := make(chan struct{}) // only start data consuming when the "testCount" tasks are all pushed into queue
		stopAt20Shutdown := make(chan struct{})  // stop and shutdown at the 20th item

		testHandler := func(data ...string) []string {
			<-startWhenAllReady
			time.Sleep(10 * time.Millisecond)
			for _, s := range data {
				mu.Lock()
				tasksQ1 = append(tasksQ1, s)
				mu.Unlock()

				if s == "task-20" {
					close(stopAt20Shutdown)
				}
			}
			return nil
		}

		q, _ := newWorkerPoolQueueForTest("pr_patch_checker_test", queueSetting, testHandler, true)
		stop := runWorkerPoolQueue(q)
		for i := range testCount {
			_ = q.Push("task-" + strconv.Itoa(i))
		}
		close(startWhenAllReady)
		<-stopAt20Shutdown // it's possible to have more than 20 tasks executed
		stop()
	}

	q1() // run some tasks and shutdown at an intermediate point

	time.Sleep(100 * time.Millisecond) // because the handler in q1 has a slight delay, we need to wait for it to finish

	q2 := func() {
		testHandler := func(data ...string) []string {
			for _, s := range data {
				mu.Lock()
				tasksQ2 = append(tasksQ2, s)
				mu.Unlock()
			}
			return nil
		}

		q, _ := newWorkerPoolQueueForTest("pr_patch_checker_test", queueSetting, testHandler, true)
		stop := runWorkerPoolQueue(q)
		assert.NoError(t, q.FlushWithContext(t.Context(), 0))
		stop()
	}

	q2() // restart the queue to continue to execute the tasks in it

	assert.NotEmpty(t, tasksQ1)
	assert.NotEmpty(t, tasksQ2)
	assert.Equal(t, testCount, len(tasksQ1)+len(tasksQ2))
}

// eventually polls until fn is true or the deadline passes, and reports what it
// last saw.
//
// The assertions below used to be `sleep N; assert`, with N tuned to the
// in-memory backend. A durable queue writes to disk before a worker can see the
// item, and under full-package load that write is not a fixed distance away —
// the tuned sleeps passed alone and failed in the suite. Polling asserts the
// state the pool reaches rather than the moment it reaches it, which is what
// these tests are actually about.
func eventually(t *testing.T, want int, get func() int, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		if last = get(); last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, want, last, msg)
}

func TestWorkerPoolQueueActiveWorkers(t *testing.T) {
	defer test.MockVariableValue(&workerIdleDuration, 300*time.Millisecond)()

	handler := func(items ...int) (unhandled []int) {
		time.Sleep(100 * time.Millisecond)
		return nil
	}

	q, _ := newWorkerPoolQueueForTest("test-workpoolqueue", setting.QueueSettings{Type: "zapdb", Datadir: t.TempDir() + "/queue", BatchLength: 1, MaxWorkers: 1, Length: 100}, handler, false)
	stop := runWorkerPoolQueue(q)
	for i := range 5 {
		assert.NoError(t, q.Push(i))
	}

	eventually(t, 1, q.GetWorkerNumber, "a worker should start")
	eventually(t, 1, q.GetWorkerActiveNumber, "that worker should become active")
	eventually(t, 0, q.GetWorkerActiveNumber, "and go idle once the items are handled")
	assert.Equal(t, 1, q.GetWorkerNumber())
	time.Sleep(workerIdleDuration)
	assert.Equal(t, 1, q.GetWorkerNumber()) // there is at least one worker after the queue begins working
	stop()

	q, _ = newWorkerPoolQueueForTest("test-workpoolqueue", setting.QueueSettings{Type: "zapdb", Datadir: t.TempDir() + "/queue", BatchLength: 1, MaxWorkers: 3, Length: 100}, handler, false)
	stop = runWorkerPoolQueue(q)
	for i := range 15 {
		assert.NoError(t, q.Push(i))
	}

	eventually(t, 3, q.GetWorkerNumber, "the pool should scale to 3 workers")
	eventually(t, 3, q.GetWorkerActiveNumber, "all 3 should become active")
	eventually(t, 0, q.GetWorkerActiveNumber, "and all go idle once handled")
	assert.Equal(t, 3, q.GetWorkerNumber())
	time.Sleep(workerIdleDuration)
	assert.Equal(t, 1, q.GetWorkerNumber()) // there is at least one worker after the queue begins working
	stop()
}

func TestWorkerPoolQueueShutdown(t *testing.T) {
	oldUnhandledItemRequeueDuration := unhandledItemRequeueDuration.Load()
	unhandledItemRequeueDuration.Store(int64(100 * time.Millisecond))
	defer unhandledItemRequeueDuration.Store(oldUnhandledItemRequeueDuration)

	// simulate a slow handler, it doesn't handle any item (all items will be pushed back to the queue)
	handlerCalled := make(chan struct{})
	handler := func(items ...int) (unhandled []int) {
		if items[0] == 0 {
			close(handlerCalled)
		}
		time.Sleep(400 * time.Millisecond)
		return items
	}

	qs := setting.QueueSettings{Type: "zapdb", Datadir: t.TempDir() + "/queue", BatchLength: 3, MaxWorkers: 4, Length: 20}
	q, _ := newWorkerPoolQueueForTest("test-workpoolqueue", qs, handler, false)
	stop := runWorkerPoolQueue(q)
	for i := 0; i < qs.Length; i++ {
		assert.NoError(t, q.Push(i))
	}
	<-handlerCalled
	time.Sleep(200 * time.Millisecond) // wait for a while to make sure all workers are active
	assert.Equal(t, 4, q.GetWorkerActiveNumber())
	stop() // stop triggers shutdown
	assert.Equal(t, 0, q.GetWorkerActiveNumber())

	// no item was ever handled, so we still get all of them again
	q, _ = newWorkerPoolQueueForTest("test-workpoolqueue", qs, handler, false)
	assert.Equal(t, 20, q.GetQueueItemNumber())
}

func TestWorkerPoolQueueWorkerIdleReset(t *testing.T) {
	defer test.MockVariableValue(&workerIdleDuration, 10*time.Millisecond)()
	defer mockBackoffDuration(5 * time.Millisecond)()

	var q *WorkerPoolQueue[int]
	var handledCount atomic.Int32
	var hasOnlyOneWorkerRunning atomic.Bool
	handler := func(items ...int) (unhandled []int) {
		handledCount.Add(int32(len(items)))
		// make each work have different duration, and check the active worker number periodically
		var activeNums []int
		for i := 0; i < 5-items[0]%2; i++ {
			time.Sleep(workerIdleDuration * 2)
			activeNums = append(activeNums, q.GetWorkerActiveNumber())
		}
		// When the queue never becomes empty, the existing workers should keep working
		// It is not 100% true at the moment because the data-race in workergroup.go is not resolved, see that TODO */
		// If the "active worker numbers" is like [2 2 ... 1 1], it means that an existing worker exited and the no new worker is started.
		if slices.Equal([]int{1, 1}, activeNums[len(activeNums)-2:]) {
			hasOnlyOneWorkerRunning.Store(true)
		}
		return nil
	}
	q, _ = newWorkerPoolQueueForTest("test-workpoolqueue", setting.QueueSettings{Type: "zapdb", Datadir: t.TempDir() + "/queue", BatchLength: 1, MaxWorkers: 2, Length: 100}, handler, false)
	stop := runWorkerPoolQueue(q)
	for i := range 100 {
		assert.NoError(t, q.Push(i))
	}
	time.Sleep(500 * time.Millisecond)
	assert.Greater(t, int(handledCount.Load()), 4) // make sure there are enough items handled during the test
	assert.False(t, hasOnlyOneWorkerRunning.Load(), "a slow handler should not block other workers from starting")
	stop()
}
