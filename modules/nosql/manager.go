// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nosql

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/hanzoai/git/modules/process"

	"github.com/hanzokv/go/v9"
)

var manager *Manager

// Manager is the nosql connection manager
type Manager struct {
	ctx      context.Context
	finished process.FinishedFunc
	mutex    sync.Mutex

	KVConnections map[string]*kvClientHolder
}

type kvClientHolder struct {
	kv.UniversalClient
	name  []string
	count int64
}

func (r *kvClientHolder) Close() error {
	return manager.CloseKVClient(r.name[0])
}

func init() {
	_ = GetManager()
}

// GetManager returns a Manager and initializes one as singleton is there's none yet
func GetManager() *Manager {
	if manager == nil {
		ctx, _, finished := process.GetManager().AddTypedContext(context.Background(), "Service: NoSQL", process.SystemProcessType, false)
		manager = &Manager{
			ctx:           ctx,
			finished:      finished,
			KVConnections: make(map[string]*kvClientHolder),
		}
	}
	return manager
}

func valToTimeDuration(vs []string) (result time.Duration) {
	var err error
	for _, v := range vs {
		result, err = time.ParseDuration(v)
		if err != nil {
			var val int
			val, err = strconv.Atoi(v)
			result = time.Duration(val)
		}
		if err == nil {
			return result
		}
	}
	return result
}
