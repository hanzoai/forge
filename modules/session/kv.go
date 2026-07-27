// Copyright 2013 Beego Authors
// Copyright 2014 The Macaron Authors
// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/nosql"

	"gitea.com/go-chi/session"
	"github.com/hanzokv/go/v9"
)

// KVStore represents a KV session store implementation.
type KVStore struct {
	c           kv.UniversalClient
	prefix, sid string
	duration    time.Duration
	lock        sync.RWMutex
	data        map[any]any
}

// NewKVStore creates and returns a KV session store.
func NewKVStore(c kv.UniversalClient, prefix, sid string, dur time.Duration, data map[any]any) *KVStore {
	return &KVStore{
		c:        c,
		prefix:   prefix,
		sid:      sid,
		duration: dur,
		data:     data,
	}
}

// Set sets value to given key in session.
func (s *KVStore) Set(key, val any) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data[key] = val
	return nil
}

// Get gets value by given key in session.
func (s *KVStore) Get(key any) any {
	s.lock.RLock()
	defer s.lock.RUnlock()

	return s.data[key]
}

// Delete delete a key from session.
func (s *KVStore) Delete(key any) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.data, key)
	return nil
}

// ID returns current session ID.
func (s *KVStore) ID() string {
	return s.sid
}

// Release releases resource and save data to provider.
func (s *KVStore) Release() error {
	// Skip encoding if the data is empty
	if len(s.data) == 0 {
		return nil
	}

	data, err := session.EncodeGob(s.data)
	if err != nil {
		return err
	}

	return s.c.Set(graceful.GetManager().HammerContext(), s.prefix+s.sid, string(data), s.duration).Err()
}

// Flush deletes all session data.
func (s *KVStore) Flush() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.data = make(map[any]any)
	return nil
}

// KVProvider represents a KV session provider implementation.
type KVProvider struct {
	c        kv.UniversalClient
	duration time.Duration
	prefix   string
}

// Init initializes KV session provider.
// configs: network=tcp,addr=:6379,password=macaron,db=0,pool_size=100,idle_timeout=180,prefix=session;
func (p *KVProvider) Init(maxlifetime int64, configs string) (err error) {
	p.duration, err = time.ParseDuration(fmt.Sprintf("%ds", maxlifetime))
	if err != nil {
		return err
	}

	uri := nosql.ToKVURI(configs)

	for k, v := range uri.Query() {
		switch k {
		case "prefix":
			p.prefix = v[0]
		}
	}

	p.c = nosql.GetManager().GetKVClient(uri.String())
	return p.c.Ping(graceful.GetManager().ShutdownContext()).Err()
}

// Read returns raw session store by session ID.
func (p *KVProvider) Read(sid string) (session.RawStore, error) {
	psid := p.prefix + sid
	if exist, err := p.Exist(sid); err == nil && !exist {
		if err := p.c.Set(graceful.GetManager().HammerContext(), psid, "", p.duration).Err(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	var data map[any]any
	kvs, err := p.c.Get(graceful.GetManager().HammerContext(), psid).Result()
	if err != nil {
		return nil, err
	}
	if len(kvs) == 0 {
		data = make(map[any]any)
	} else {
		data, err = session.DecodeGob([]byte(kvs))
		if err != nil {
			return nil, err
		}
	}

	return NewKVStore(p.c, p.prefix, sid, p.duration, data), nil
}

// Exist returns true if session with given ID exists.
func (p *KVProvider) Exist(sid string) (bool, error) {
	v, err := p.c.Exists(graceful.GetManager().HammerContext(), p.prefix+sid).Result()
	return err == nil && v == 1, err
}

// Destroy deletes a session by session ID.
func (p *KVProvider) Destroy(sid string) error {
	return p.c.Del(graceful.GetManager().HammerContext(), p.prefix+sid).Err()
}

// Regenerate regenerates a session store from old session ID to new one.
func (p *KVProvider) Regenerate(oldsid, sid string) (_ session.RawStore, err error) {
	poldsid := p.prefix + oldsid
	psid := p.prefix + sid

	if exist, err := p.Exist(sid); err != nil {
		return nil, err
	} else if exist {
		return nil, fmt.Errorf("new sid '%s' already exists", sid)
	}
	if exist, err := p.Exist(oldsid); err == nil && !exist {
		// Make a fake old session.
		if err := p.c.Set(graceful.GetManager().HammerContext(), poldsid, "", p.duration).Err(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	// do not use Rename here, because the old sid and new sid may be in different KV cluster slot.
	kvs, err := p.c.Get(graceful.GetManager().HammerContext(), poldsid).Result()
	if err != nil {
		return nil, err
	}

	if err = p.c.Del(graceful.GetManager().HammerContext(), poldsid).Err(); err != nil {
		return nil, err
	}

	if err = p.c.Set(graceful.GetManager().HammerContext(), psid, kvs, p.duration).Err(); err != nil {
		return nil, err
	}

	var data map[any]any
	if len(kvs) == 0 {
		data = make(map[any]any)
	} else {
		data, err = session.DecodeGob([]byte(kvs))
		if err != nil {
			return nil, err
		}
	}

	return NewKVStore(p.c, p.prefix, sid, p.duration, data), nil
}

// Count counts and returns number of sessions.
func (p *KVProvider) Count() (int, error) {
	size, err := p.c.DBSize(graceful.GetManager().HammerContext()).Result()
	return int(size), err
}

// GC calls GC to clean expired sessions.
func (*KVProvider) GC() {}

func init() {
	session.Register("kv", &KVProvider{})
}
