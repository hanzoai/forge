// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2014 The Macaron Authors
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package cache

import (
	"encoding/gob"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_Cacher(t *testing.T) {
	t.Run("Register invalid adapter", func(t *testing.T) {
		t.Run("Adatper not exists", func(t *testing.T) {
			opt := Options{
				Adapter: "fake",
			}
			_, err := NewCacher(opt)
			assert.Error(t, err)
		})

		t.Run("Provider value is nil", func(t *testing.T) {
			defer func() {
				assert.NotNil(t, recover())
			}()

			Register("fake", nil)
		})

		t.Run("Register twice", func(t *testing.T) {
			defer func() {
				assert.NotNil(t, recover())
			}()

			Register("memory", &MemoryCacher{})
		})
	})
}

func GenerateAdapterTest(opt Options) func(t *testing.T) {
	return func(t *testing.T) {
		t.Run("Basic operations", func(t *testing.T) {
			c, err := NewCacher(opt)
			assert.NoError(t, err)

			assert.NoError(t, c.Put("uname", "some-user-name", 1))
			assert.NoError(t, c.Put("uname2", "unknwon2", 1))
			assert.True(t, c.IsExist("uname"))

			assert.Nil(t, c.Get("404"))
			assert.EqualValues(t, "some-user-name", c.Get("uname"))

			time.Sleep(1 * time.Second)
			assert.False(t, c.IsExist("uname"))
			assert.Nil(t, c.Get("uname"))
			time.Sleep(1 * time.Second)
			assert.Nil(t, c.Get("uname2"))

			assert.NoError(t, c.Put("uname", "some-user-name", 0))
			assert.NoError(t, c.Delete("uname"))
			assert.Nil(t, c.Get("uname"))

			assert.NoError(t, c.Put("uname", "some-user-name", 0))
			assert.NoError(t, c.Flush())
			assert.Nil(t, c.Get("uname"))

			gob.Register(opt)
			assert.NoError(t, c.Put("struct", opt, 0))
		})

		t.Run("Increase and decrease operations", func(t *testing.T) {
			c, err := NewCacher(opt)
			assert.NoError(t, err)

			assert.Error(t, c.Incr("404"))
			assert.Error(t, c.Decr("404"))

			assert.NoError(t, c.Put("int", 0, 0))
			assert.NoError(t, c.Put("int32", int32(0), 0))
			assert.NoError(t, c.Put("int64", int64(0), 0))
			assert.NoError(t, c.Put("uint", uint(0), 0))
			assert.NoError(t, c.Put("uint32", uint32(0), 0))
			assert.NoError(t, c.Put("uint64", uint64(0), 0))
			assert.NoError(t, c.Put("string", "hi", 0))

			assert.Error(t, c.Decr("uint"))
			assert.Error(t, c.Decr("uint32"))
			assert.Error(t, c.Decr("uint64"))

			assert.NoError(t, c.Incr("int"))
			assert.NoError(t, c.Incr("int32"))
			assert.NoError(t, c.Incr("int64"))
			assert.NoError(t, c.Incr("uint"))
			assert.NoError(t, c.Incr("uint32"))
			assert.NoError(t, c.Incr("uint64"))

			assert.NoError(t, c.Decr("int"))
			assert.NoError(t, c.Decr("int32"))
			assert.NoError(t, c.Decr("int64"))
			assert.NoError(t, c.Decr("uint"))
			assert.NoError(t, c.Decr("uint32"))
			assert.NoError(t, c.Decr("uint64"))

			assert.Error(t, c.Incr("string"))
			assert.Error(t, c.Decr("string"))

			assert.EqualValues(t, 0, c.Get("int"))
			assert.EqualValues(t, 0, c.Get("int32"))
			assert.EqualValues(t, 0, c.Get("int64"))
			assert.EqualValues(t, 0, c.Get("uint"))
			assert.EqualValues(t, 0, c.Get("uint32"))
			assert.EqualValues(t, 0, c.Get("uint64"))
		})
	}
}
