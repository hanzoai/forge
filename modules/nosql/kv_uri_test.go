// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nosql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToKVURI(t *testing.T) {
	tests := []struct {
		name       string
		connection string
		want       string
	}{
		{
			name:       "old_default",
			connection: "addrs=127.0.0.1:6379 db=0",
			want:       "kv://127.0.0.1:6379/0",
		},
		{
			name:       "old_macaron_session_default",
			connection: "network=tcp,addr=127.0.0.1:6379,password=macaron,db=0,pool_size=100,idle_timeout=180",
			want:       "kv://:macaron@127.0.0.1:6379/0?idle_timeout=180s&pool_size=100",
		},
		{
			name:       "old_cluster",
			connection: "addrs=127.0.0.1:6379,127.0.0.1:6380 db=0",
			want:       "kv+cluster://127.0.0.1:6379,127.0.0.1:6380/0",
		},
		{
			name:       "old_socket",
			connection: "network=unix addr=/var/run/kv.sock",
			want:       "kv+socket:///var/run/kv.sock?db=%2F0",
		},
		{ // an already-KV URI passes through untouched, whichever member of the scheme family it uses
			name:       "kv_uri",
			connection: "kv://127.0.0.1:6379/0",
			want:       "kv://127.0.0.1:6379/0",
		},
		{
			name:       "kvs_uri",
			connection: "kvs://127.0.0.1:6379/0",
			want:       "kvs://127.0.0.1:6379/0",
		},
		{
			name:       "kv_sentinel_uri",
			connection: "kv+sentinel://127.0.0.1:26379/0?mastername=mymaster",
			want:       "kv+sentinel://127.0.0.1:26379/0?mastername=mymaster",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToKVURI(tt.connection)
			require.NotNil(t, got)
			assert.Equal(t, tt.want, got.String())
		})
	}
}
