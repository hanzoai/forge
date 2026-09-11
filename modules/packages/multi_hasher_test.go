// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package packages

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	expectedMD5    = "228410f5c71f8af2b1266b70fa7824eb"
	expectedSHA1   = "52b7362eed5e398bad39dafb5ad68515e6547a3a"
	expectedSHA256 = "71b41d6dd48dc58eba8f5cf9edf30fef6597fdf285a521bb8fcbad4b3d50887d"
	expectedSHA512 = "b2ccaa58071577713a0841e23b7c277da321f090e858afdd3e2f1568687c6140c1a3df4759355d7fd2d4a39eab9bf19c7197bee4c73a6814b206324025360db5"
)

func TestMultiHasherSums(t *testing.T) {
	t.Run("Sums", func(t *testing.T) {
		h := NewMultiHasher()
		h.Write([]byte("forge"))

		hashMD5, hashSHA1, hashSHA256, hashSHA512 := h.Sums()

		assert.Equal(t, expectedMD5, hex.EncodeToString(hashMD5))
		assert.Equal(t, expectedSHA1, hex.EncodeToString(hashSHA1))
		assert.Equal(t, expectedSHA256, hex.EncodeToString(hashSHA256))
		assert.Equal(t, expectedSHA512, hex.EncodeToString(hashSHA512))
	})

	t.Run("State", func(t *testing.T) {
		h := NewMultiHasher()
		h.Write([]byte("for"))

		state, err := h.MarshalBinary()
		assert.NoError(t, err)

		h2 := NewMultiHasher()
		err = h2.UnmarshalBinary(state)
		assert.NoError(t, err)

		h2.Write([]byte("ge"))

		hashMD5, hashSHA1, hashSHA256, hashSHA512 := h2.Sums()

		assert.Equal(t, expectedMD5, hex.EncodeToString(hashMD5))
		assert.Equal(t, expectedSHA1, hex.EncodeToString(hashSHA1))
		assert.Equal(t, expectedSHA256, hex.EncodeToString(hashSHA256))
		assert.Equal(t, expectedSHA512, hex.EncodeToString(hashSHA512))
	})
}
