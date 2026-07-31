// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/luxfi/database"
	"github.com/luxfi/database/zapdb"
)

// baseZapDB is the durable queue backend: an embedded FIFO on ZapDB.
//
// It replaces the leveldb/levelqueue pair. The requirement is an ordered,
// crash-safe queue that needs no server — the forge must start and index and
// deliver webhooks on a single box — and ZapDB is the embedded store this stack
// already runs (kms, cloud, mpc). leveldb was a second embedded engine doing
// the same job, reached through a third-party FIFO wrapper.
//
// It talks to ZapDB through github.com/luxfi/database rather than importing
// github.com/luxfi/zapdb directly. That is not ceremony: holding a *zapdb.DB
// pins this queue to one engine, and two engines able to open one directory is
// precisely what took git.hanzo.ai down — each engine's recovery deletes the
// files the other's manifest does not list, so goleveldb ate ZapDB's tables and
// the forge died at boot on a dangling manifest. One interface, one engine
// chosen in one place, and swapping it is a change here and nowhere else.
//
// Layout, all under one prefix per queue so RemoveAll is a single DropPrefix:
//
//	<name>/i/<be-uint64>  -> item payload   (ordered: iteration IS FIFO order)
//	<name>/u/<payload>    -> {}             (unique index, only when unique)
//
// Ordering comes from a persisted sequence rather than a counter in memory, so
// restarting mid-queue does not hand out an index that is already on disk and
// silently overwrite an unread item.
type baseZapDB struct {
	db  database.Database
	dir string

	// The three capabilities this queue needs beyond plain key/value. They are
	// optional interfaces, so they are asserted ONCE at open: a backend that
	// cannot provide them fails to construct the queue, instead of failing the
	// first time an item is pushed.
	txn     database.Transactional
	seqr    database.Sequencer
	dropper database.PrefixDropper

	seq    database.Sequence
	cfg    *BaseConfig
	unique bool

	// mu serialises PopItem. Reading the head and deleting it must be one step:
	// two poppers racing between the read and the delete would both return the
	// same item, and a queue that delivers a webhook twice is worse than one
	// that is briefly slow.
	mu sync.Mutex
}

// ZapDB takes an EXCLUSIVE lock on its directory, and every queue in this
// process — issue indexer, code indexer, mail, webhook delivery, push update —
// is configured with the same DATADIR. Opening per queue therefore works
// exactly once and then fails with "Cannot acquire directory lock", which
// surfaces far from the cause as a half-built queue whose run loop nil-panics.
//
// So one handle per directory, reference counted, closed when the last queue
// using it closes. This is what the leveldb backend got from the nosql manager;
// it is stated here instead of inherited.
var (
	sharedMu sync.Mutex
	sharedDB = map[string]*sharedZapDB{}
)

type sharedZapDB struct {
	db   database.Database
	refs int
}

func openSharedZapDB(dir string) (database.Database, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sh := sharedDB[dir]; sh != nil {
		sh.refs++
		return sh.db, nil
	}
	// nil config selects the backend's defaults and silences its logger — the
	// queue logs its own lifecycle and ZapDB's is noise here. nil metrics is
	// accepted; the backend does not register any.
	db, err := zapdb.New(dir, nil, "queue", nil)
	if err != nil {
		return nil, err
	}
	sharedDB[dir] = &sharedZapDB{db: db, refs: 1}
	return db, nil
}

func closeSharedZapDB(dir string) error {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	sh := sharedDB[dir]
	if sh == nil {
		return nil
	}
	sh.refs--
	if sh.refs > 0 {
		return nil // another queue is still using this directory
	}
	delete(sharedDB, dir)
	return sh.db.Close()
}

var _ baseQueue = (*baseZapDB)(nil)

func newBaseZapDBGeneric(cfg *BaseConfig, unique bool) (baseQueue, error) {
	dir := cfg.DataFullDir
	if dir == "" {
		return nil, errors.New("queue: zapdb backend needs DATADIR")
	}
	dir = filepath.Clean(dir)
	db, err := openSharedZapDB(dir)
	if err != nil {
		return nil, err
	}

	q := &baseZapDB{db: db, dir: dir, cfg: cfg, unique: unique}
	// Assert every optional capability up front, and name the missing one: a
	// bare "type assertion failed" here would be read as a bug in this file
	// rather than as "the configured backend cannot back a durable queue".
	var ok bool
	if q.txn, ok = db.(database.Transactional); !ok {
		_ = closeSharedZapDB(dir)
		return nil, fmt.Errorf("queue: backend %T is not database.Transactional; a queue needs atomic read-then-delete", db)
	}
	if q.seqr, ok = db.(database.Sequencer); !ok {
		_ = closeSharedZapDB(dir)
		return nil, fmt.Errorf("queue: backend %T is not database.Sequencer; a queue needs crash-safe ordering", db)
	}
	if q.dropper, ok = db.(database.PrefixDropper); !ok {
		_ = closeSharedZapDB(dir)
		return nil, fmt.Errorf("queue: backend %T is not database.PrefixDropper; RemoveAll needs it", db)
	}

	q.seq, err = q.seqr.GetSequence([]byte(cfg.QueueFullName+"/seq"), 100)
	if err != nil {
		_ = closeSharedZapDB(dir)
		return nil, err
	}
	return q, nil
}

func newBaseZapDBSimple(cfg *BaseConfig) (baseQueue, error) {
	return newBaseZapDBGeneric(cfg, false)
}

func newBaseZapDBUnique(cfg *BaseConfig) (baseQueue, error) {
	return newBaseZapDBGeneric(cfg, true)
}

func (q *baseZapDB) itemPrefix() []byte   { return []byte(q.cfg.QueueFullName + "/i/") }
func (q *baseZapDB) uniquePrefix() []byte { return []byte(q.cfg.QueueFullName + "/u/") }

func (q *baseZapDB) itemKey(idx uint64) []byte {
	// Big-endian so lexicographic key order is numeric order — that is the only
	// reason iteration yields FIFO.
	k := make([]byte, 0, len(q.itemPrefix())+8)
	k = append(k, q.itemPrefix()...)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], idx)
	return append(k, b[:]...)
}

func (q *baseZapDB) uniqueKey(data []byte) []byte {
	return append(q.uniquePrefix(), data...)
}

func (q *baseZapDB) PushItem(ctx context.Context, data []byte) error {
	if q.unique {
		has, err := q.HasItem(ctx, data)
		if err != nil {
			return err
		}
		if has {
			return ErrAlreadyInQueue
		}
	}
	idx, err := q.seq.Next()
	if err != nil {
		return err
	}
	return q.txn.Update(func(txn database.Txn) error {
		if err := txn.Put(q.itemKey(idx), data); err != nil {
			return err
		}
		if q.unique {
			return txn.Put(q.uniqueKey(data), []byte{})
		}
		return nil
	})
}

func (q *baseZapDB) PopItem(ctx context.Context) ([]byte, error) {
	// PopItem BLOCKS until an item is available or ctx ends — that is the
	// contract every other backend implements via backoffRetErr, and the worker
	// pool depends on it: returning (nil, nil) on an empty queue instead turns
	// the worker loop into a spin and makes FlushWithContext report the
	// cancellation as an error.
	return backoffRetErr(ctx, backoffBegin, backoffUpper, infiniteTimerC,
		func() (retry bool, data []byte, err error) {
			q.mu.Lock()
			defer q.mu.Unlock()

			err = q.txn.Update(func(txn database.Txn) error {
				it := txn.NewIteratorWithPrefix(q.itemPrefix())

				var key, val []byte
				if it.Next() {
					// Key/Value are only valid until Release, and the docs make no
					// promise about the buffers outliving the iterator — copy both
					// before releasing rather than aliasing into it.
					key = append([]byte(nil), it.Key()...)
					val = append([]byte(nil), it.Value()...)
				}
				iterErr := it.Error()
				// Release BEFORE mutating the range being iterated. Deleting under
				// a live iterator is the one thing backends define least
				// consistently, and the queue does not need to hold it.
				it.Release()
				if iterErr != nil {
					return iterErr
				}
				if key == nil {
					return nil // empty; reported as retry below
				}

				if derr := txn.Delete(key); derr != nil {
					return derr
				}
				if q.unique {
					if derr := txn.Delete(q.uniqueKey(val)); derr != nil {
						return derr
					}
				}
				data = val
				return nil
			})
			if err != nil {
				return false, nil, err
			}
			return data == nil, data, nil
		})
}

func (q *baseZapDB) HasItem(ctx context.Context, data []byte) (bool, error) {
	if !q.unique {
		// A non-unique queue has no membership index, and scanning every item on
		// each push would make PushItem O(n). Matches the leveldb backend.
		return false, nil
	}
	found := false
	err := q.txn.View(func(txn database.Txn) error {
		_, err := txn.Get(q.uniqueKey(data))
		if errors.Is(err, database.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return found, err
}

func (q *baseZapDB) Len(ctx context.Context) (int, error) {
	n := 0
	err := q.txn.View(func(txn database.Txn) error {
		// Counting keys, not reading payloads. The engine-specific
		// "don't prefetch values" hint is not part of the interface, so this
		// trades a little read amplification for not being pinned to one engine.
		it := txn.NewIteratorWithPrefix(q.itemPrefix())
		defer it.Release()
		for it.Next() {
			n++
		}
		return it.Error()
	})
	return n, err
}

func (q *baseZapDB) RemoveAll(ctx context.Context) error {
	return q.dropper.DropPrefix(q.itemPrefix(), q.uniquePrefix())
}

func (q *baseZapDB) Close() error {
	// Release the sequence first: it holds a lease of unissued indices, and
	// releasing writes the high-water mark back. Skipping it would leak the
	// bandwidth window on every restart, so indices would march forward forever.
	if q.seq != nil {
		_ = q.seq.Release()
	}
	return closeSharedZapDB(q.dir)
}
