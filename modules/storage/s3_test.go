// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"testing"

	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
)

func TestS3Storage(t *testing.T) {
	endpoint := test.ExternalServiceHTTP(t, "TEST_S3_ENDPOINT", "s3:9000")
	storageType := setting.S3StorageType
	config := &setting.Storage{
		S3Config: setting.S3StorageConfig{
			Endpoint:        endpoint,
			AccessKeyID:     "123456",
			SecretAccessKey: "12345678",
			Bucket:          "gitea",
			Location:        "us-east-1",
		},
	}
	table := []struct {
		name string
		test func(t *testing.T, typStr Type, cfg *setting.Storage)
	}{
		{
			name: "iterator",
			test: testStorageIterator,
		},
		{
			name: "testBlobStorageURLContentTypeAndDisposition",
			test: testBlobStorageURLContentTypeAndDisposition,
		},
	}
	for _, entry := range table {
		t.Run(entry.name, func(t *testing.T) {
			entry.test(t, storageType, config)
		})
	}
}

func TestS3StoragePath(t *testing.T) {
	m := &S3Storage{basePath: ""}
	assert.Empty(t, m.buildS3Path("/"))
	assert.Empty(t, m.buildS3Path("."))
	assert.Equal(t, "a", m.buildS3Path("/a"))
	assert.Equal(t, "a/b", m.buildS3Path("/a/b/"))
	assert.Empty(t, m.buildS3DirPrefix(""))
	assert.Equal(t, "a/", m.buildS3DirPrefix("/a/"))

	m = &S3Storage{basePath: "/"}
	assert.Empty(t, m.buildS3Path("/"))
	assert.Empty(t, m.buildS3Path("."))
	assert.Equal(t, "a", m.buildS3Path("/a"))
	assert.Equal(t, "a/b", m.buildS3Path("/a/b/"))
	assert.Empty(t, m.buildS3DirPrefix(""))
	assert.Equal(t, "a/", m.buildS3DirPrefix("/a/"))

	m = &S3Storage{basePath: "/base"}
	assert.Equal(t, "base", m.buildS3Path("/"))
	assert.Equal(t, "base", m.buildS3Path("."))
	assert.Equal(t, "base/a", m.buildS3Path("/a"))
	assert.Equal(t, "base/a/b", m.buildS3Path("/a/b/"))
	assert.Equal(t, "base/", m.buildS3DirPrefix(""))
	assert.Equal(t, "base/a/", m.buildS3DirPrefix("/a/"))

	m = &S3Storage{basePath: "/base/"}
	assert.Equal(t, "base", m.buildS3Path("/"))
	assert.Equal(t, "base", m.buildS3Path("."))
	assert.Equal(t, "base/a", m.buildS3Path("/a"))
	assert.Equal(t, "base/a/b", m.buildS3Path("/a/b/"))
	assert.Equal(t, "base/", m.buildS3DirPrefix(""))
	assert.Equal(t, "base/a/", m.buildS3DirPrefix("/a/"))
}

func TestS3StorageBadRequest(t *testing.T) {
	endpoint := test.ExternalServiceHTTP(t, "TEST_S3_ENDPOINT", "s3:9000")
	cfg := &setting.Storage{
		S3Config: setting.S3StorageConfig{
			Endpoint:        endpoint,
			AccessKeyID:     "123456",
			SecretAccessKey: "invalid-secret",
			Bucket:          "bucket",
			Location:        "us-east-1",
		},
	}
	_, err := NewStorage(setting.S3StorageType, cfg)
	assert.ErrorContains(t, err, "ObjectStorage.HeadBucket: endpoint="+endpoint)
}

func TestS3Credentials(t *testing.T) {
	const (
		ExpectedAccessKey       = "ExampleAccessKeyID"
		ExpectedSecretAccessKey = "ExampleSecretAccessKeyID"
	)

	t.Run("Static Credentials", func(t *testing.T) {
		cfg := setting.S3StorageConfig{
			AccessKeyID:     ExpectedAccessKey,
			SecretAccessKey: ExpectedSecretAccessKey,
		}
		v, err := buildS3Credentials(cfg).Retrieve(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, ExpectedAccessKey, v.AccessKeyID)
		assert.Equal(t, ExpectedSecretAccessKey, v.SecretAccessKey)
	})
}
