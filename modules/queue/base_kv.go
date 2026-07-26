// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"context"
	"sync"
	"time"

	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/nosql"

	"github.com/hanzokv/go/v9"
)

type baseKV struct {
	client   kv.UniversalClient
	isUnique bool
	cfg      *BaseConfig

	mu sync.Mutex // the old implementation is not thread-safe, the queue operation and set operation should be protected together
}

var _ baseQueue = (*baseKV)(nil)

func newBaseKVGeneric(cfg *BaseConfig, unique bool) (baseQueue, error) {
	client := nosql.GetManager().GetKVClient(cfg.ConnStr)

	var err error
	for range 10 {
		err = client.Ping(graceful.GetManager().ShutdownContext()).Err()
		if err == nil {
			break
		}
		log.Warn("KV is not ready, waiting for 1 second to retry: %v", err)
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, err
	}

	return &baseKV{cfg: cfg, client: client, isUnique: unique}, nil
}

func newBaseKVSimple(cfg *BaseConfig) (baseQueue, error) {
	return newBaseKVGeneric(cfg, false)
}

func newBaseKVUnique(cfg *BaseConfig) (baseQueue, error) {
	return newBaseKVGeneric(cfg, true)
}

func (q *baseKV) PushItem(ctx context.Context, data []byte) error {
	return backoffErr(ctx, backoffBegin, backoffUpper, time.After(pushBlockTime), func() (retry bool, err error) {
		q.mu.Lock()
		defer q.mu.Unlock()

		cnt, err := q.client.LLen(ctx, q.cfg.QueueFullName).Result()
		if err != nil {
			return false, err
		}
		if int(cnt) >= q.cfg.Length {
			return true, nil
		}

		if q.isUnique {
			added, err := q.client.SAdd(ctx, q.cfg.SetFullName, data).Result()
			if err != nil {
				return false, err
			}
			if added == 0 {
				return false, ErrAlreadyInQueue
			}
		}
		return false, q.client.RPush(ctx, q.cfg.QueueFullName, data).Err()
	})
}

func (q *baseKV) PopItem(ctx context.Context) ([]byte, error) {
	return backoffRetErr(ctx, backoffBegin, backoffUpper, infiniteTimerC, func() (retry bool, data []byte, err error) {
		q.mu.Lock()
		defer q.mu.Unlock()

		data, err = q.client.LPop(ctx, q.cfg.QueueFullName).Bytes()
		if err == kv.Nil {
			return true, nil, nil
		}
		if err != nil {
			return true, nil, nil
		}
		if q.isUnique {
			// the data has been popped, even if there is any error we can't do anything
			_ = q.client.SRem(ctx, q.cfg.SetFullName, data).Err()
		}
		return false, data, err
	})
}

func (q *baseKV) HasItem(ctx context.Context, data []byte) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.isUnique {
		return false, nil
	}
	return q.client.SIsMember(ctx, q.cfg.SetFullName, data).Result()
}

func (q *baseKV) Len(ctx context.Context) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cnt, err := q.client.LLen(ctx, q.cfg.QueueFullName).Result()
	return int(cnt), err
}

func (q *baseKV) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.client.Close()
}

func (q *baseKV) RemoveAll(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	c1 := q.client.Del(ctx, q.cfg.QueueFullName)
	// the "set" must be cleared after the "list" because there is no transaction.
	// it's better to have duplicate items than losing items.
	c2 := q.client.Del(ctx, q.cfg.SetFullName)
	if c1.Err() != nil {
		return c1.Err()
	}
	if c2.Err() != nil {
		return c2.Err()
	}
	return nil // actually, checking errors doesn't make sense here because the state could be out-of-sync
}
