// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package queue

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hanzoai/git/modules/nosql"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitKVReady(conn string, dur time.Duration) (ready bool) {
	ctxTimed, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	for t := time.Now(); ; time.Sleep(50 * time.Millisecond) {
		ret := nosql.GetManager().GetKVClient(conn).Ping(ctxTimed)
		if ret.Err() == nil {
			return true
		}
		if time.Since(t) > dur {
			return false
		}
	}
}

// kvServerCmd spawns a local KV server for developers who don't have one running; "redis-server" is the
// name of the program on PATH, not a vocabulary choice.
func kvServerCmd(t *testing.T) *exec.Cmd {
	kvServerProg, err := exec.LookPath("redis-server")
	if err != nil {
		return nil
	}
	c := &exec.Cmd{
		Path:   kvServerProg,
		Args:   []string{kvServerProg, "--bind", "127.0.0.1", "--port", "6379"},
		Dir:    t.TempDir(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return c
}

func TestBaseKV(t *testing.T) {
	var kvServer *exec.Cmd
	defer func() {
		if kvServer != nil {
			_ = kvServer.Process.Signal(os.Interrupt)
			_ = kvServer.Wait()
		}
	}()
	if !waitKVReady("kv://127.0.0.1:6379/0", 0) {
		kvServer = kvServerCmd(t)
		if kvServer == nil && test.AllowSkipExternalService() {
			t.Skip("KV server command not found, skipped")
		}
		require.NotNil(t, kvServer)
		assert.NoError(t, kvServer.Start())
		require.True(t, waitKVReady("kv://127.0.0.1:6379/0", 5*time.Second), "start KV server")
	}

	testQueueBasic(t, newBaseKVSimple, toBaseConfig("baseKV", setting.QueueSettings{Length: 10}), false)
	testQueueBasic(t, newBaseKVUnique, toBaseConfig("baseKVUnique", setting.QueueSettings{Length: 10}), true)
}
