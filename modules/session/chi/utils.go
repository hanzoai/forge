// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2013 Beego Authors
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

package session

import (
	"bytes"
	"encoding/gob"
)

func init() {
	gob.Register([]interface{}{})
	gob.Register(map[int]interface{}{})
	gob.Register(map[string]interface{}{})
	gob.Register(map[interface{}]interface{}{})
	gob.Register(map[string]string{})
	gob.Register(map[int]string{})
	gob.Register(map[int]int{})
	gob.Register(map[int]int64{})
}

// EncodeGob encodes obj with gob
func EncodeGob(obj map[interface{}]interface{}) ([]byte, error) {
	// Don't automatically register any types here, because if the data is in session and server restarts,
	// the types are not registered anymore when decoding, then it will cause panic.
	buf := bytes.NewBuffer(nil)
	err := gob.NewEncoder(buf).Encode(obj)
	return buf.Bytes(), err
}

// DecodeGob decodes bytes to obj
func DecodeGob(encoded []byte) (out map[interface{}]interface{}, err error) {
	buf := bytes.NewBuffer(encoded)
	err = gob.NewDecoder(buf).Decode(&out)
	return out, err
}
