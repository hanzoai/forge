// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package storage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/util"

	minio "github.com/hanzoai/s3-go"
	"github.com/hanzoai/s3-go/pkg/credentials"
)

var _ ObjectStorage = &S3Storage{}

type s3Object struct {
	*minio.Object
}

func (m *s3Object) Stat() (os.FileInfo, error) {
	oi, err := m.Object.Stat()
	if err != nil {
		return nil, convertS3Err(err)
	}

	return &s3FileInfo{oi}, nil
}

// S3Storage returns an S3 bucket storage backed by the hanzoai/s3-go client
type S3Storage struct {
	cfg      *setting.S3StorageConfig
	ctx      context.Context
	client   *minio.Client
	bucket   string
	basePath string
}

func convertS3Err(err error, optMsg ...string) error {
	if err == nil {
		return nil
	}

	wrapErr := func(err error) error {
		if len(optMsg) == 0 {
			return err
		}
		return fmt.Errorf("%s: %w", optMsg[0], err)
	}

	errResp, ok := errors.AsType[minio.ErrorResponse](err)
	if !ok {
		return wrapErr(err)
	}

	// Convert two responses to standard analogues
	switch errResp.Code {
	case "NoSuchKey":
		return wrapErr(os.ErrNotExist)
	case "AccessDenied":
		return wrapErr(os.ErrPermission)
	}

	return wrapErr(err)
}

// NewS3Storage returns an S3 storage
func NewS3Storage(ctx context.Context, cfg *setting.Storage) (ObjectStorage, error) {
	config := cfg.S3Config
	log.Info("Creating S3 storage at %s:%s with base path %s", config.Endpoint, config.Bucket, config.BasePath)
	if config.ChecksumAlgorithm != "" && config.ChecksumAlgorithm != "default" && config.ChecksumAlgorithm != "md5" {
		return nil, fmt.Errorf("invalid S3 checksum algorithm: %s", config.ChecksumAlgorithm)
	}

	var lookup minio.BucketLookupType
	switch config.BucketLookUpType {
	case "auto", "":
		lookup = minio.BucketLookupAuto
	case "dns":
		lookup = minio.BucketLookupDNS
	case "path":
		lookup = minio.BucketLookupPath
	default:
		return nil, fmt.Errorf("invalid S3 bucket lookup type: %s", config.BucketLookUpType)
	}

	// The request error message is something like:
	// * "The request signature we calculated does not match the signature you provided. Check your key and signing method."
	// It doesn't contain useful information to site admin, so here we wrap the error with our error message
	// to tell the site admin what is the problem.
	makeErrMsg := func(hint string) string {
		return fmt.Sprintf("ObjectStorage.%s: endpoint=%s, location=%s, bucket=%s", hint, config.Endpoint, config.Location, config.Bucket)
	}
	s3Client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:        buildS3Credentials(config),
		Secure:       config.UseSSL,
		Transport:    &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: config.InsecureSkipVerify}},
		Region:       config.Location,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, convertS3Err(err, makeErrMsg("NewClient"))
	}

	// Check to see if we already own this bucket
	exists, err := s3Client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, convertS3Err(err, makeErrMsg("BucketExists"))
	}

	// If the bucket doesn't exist, try to create one
	if !exists {
		if err := s3Client.MakeBucket(ctx, config.Bucket, minio.MakeBucketOptions{
			Region: config.Location,
		}); err != nil {
			return nil, convertS3Err(err, makeErrMsg("MakeBucket"))
		}
	}

	return &S3Storage{
		cfg:      &config,
		ctx:      ctx,
		client:   s3Client,
		bucket:   config.Bucket,
		basePath: config.BasePath,
	}, nil
}

func (m *S3Storage) buildS3Path(p string) string {
	p = strings.TrimPrefix(util.PathJoinRelX(m.basePath, p), "/") // object store doesn't use slash for root path
	if p == "." {
		p = "" // object store doesn't use dot as relative path
	}
	return p
}

func (m *S3Storage) buildS3DirPrefix(p string) string {
	// ending slash is required for avoiding matching like "foo/" and "foobar/" with prefix "foo"
	p = m.buildS3Path(p) + "/"
	if p == "/" {
		p = "" // object store doesn't use slash for root path
	}
	return p
}

func buildS3Credentials(config setting.S3StorageConfig) *credentials.Credentials {
	// If static credentials are provided, use those
	if config.AccessKeyID != "" {
		return credentials.NewStaticV4(config.AccessKeyID, config.SecretAccessKey, "")
	}

	// Otherwise, fall back to the standard AWS credential chain for S3 access
	chain := []credentials.Provider{
		// configure based upon AWS_ prefixed environment variables
		&credentials.EnvAWS{},
		// read credentials from AWS_SHARED_CREDENTIALS_FILE
		// environment variable, or default credentials file
		&credentials.FileAWSCredentials{},
		// read IAM role from EC2 metadata endpoint if available
		&credentials.IAM{
			// passing in an empty Endpoint lets the IAM Provider
			// decide which endpoint to resolve internally
			Endpoint: config.IamEndpoint,
			Client: &http.Client{
				Transport: http.DefaultTransport,
			},
		},
	}
	return credentials.NewChainCredentials(chain)
}

// Open opens a file
func (m *S3Storage) Open(path string) (Object, error) {
	opts := minio.GetObjectOptions{}
	object, err := m.client.GetObject(m.ctx, m.bucket, m.buildS3Path(path), opts)
	if err != nil {
		return nil, convertS3Err(err)
	}
	return &s3Object{object}, nil
}

// Save saves a file to S3
func (m *S3Storage) Save(path string, r io.Reader, size int64) (int64, error) {
	uploadInfo, err := m.client.PutObject(
		m.ctx,
		m.bucket,
		m.buildS3Path(path),
		r,
		size,
		minio.PutObjectOptions{
			ContentType: "application/octet-stream",
			// some storages like:
			// * https://developers.cloudflare.com/r2/api/s3/api/
			// * https://www.backblaze.com/b2/docs/s3_compatible_api.html
			// do not support "x-amz-checksum-algorithm" header, so use legacy MD5 checksum
			SendContentMd5: m.cfg.ChecksumAlgorithm == "md5",
		},
	)
	if err != nil {
		return 0, convertS3Err(err)
	}
	return uploadInfo.Size, nil
}

type s3FileInfo struct {
	minio.ObjectInfo
}

func (m s3FileInfo) Name() string {
	return path.Base(m.ObjectInfo.Key)
}

func (m s3FileInfo) Size() int64 {
	return m.ObjectInfo.Size
}

func (m s3FileInfo) ModTime() time.Time {
	return m.LastModified
}

func (m s3FileInfo) IsDir() bool {
	return strings.HasSuffix(m.ObjectInfo.Key, "/")
}

func (m s3FileInfo) Mode() os.FileMode {
	return os.ModePerm
}

func (m s3FileInfo) Sys() any {
	return nil
}

// Stat returns the stat information of the object
func (m *S3Storage) Stat(path string) (os.FileInfo, error) {
	info, err := m.client.StatObject(
		m.ctx,
		m.bucket,
		m.buildS3Path(path),
		minio.StatObjectOptions{},
	)
	if err != nil {
		return nil, convertS3Err(err)
	}
	return &s3FileInfo{info}, nil
}

// Delete delete a file
func (m *S3Storage) Delete(path string) error {
	err := m.client.RemoveObject(m.ctx, m.bucket, m.buildS3Path(path), minio.RemoveObjectOptions{})

	return convertS3Err(err)
}

func (m *S3Storage) ServeDirectURL(storePath, name, method string, opt *ServeDirectOptions) (*url.URL, error) {
	reqParams := url.Values{}

	param := prepareServeDirectOptions(opt, name)
	// S3 presigning does not ignore empty params
	if param.ContentType != "" {
		reqParams.Set("response-content-type", param.ContentType)
	}
	if param.ContentDisposition != "" {
		reqParams.Set("response-content-disposition", param.ContentDisposition)
	}

	expires := 5 * time.Minute
	if method == http.MethodHead {
		u, err := m.client.PresignedHeadObject(m.ctx, m.bucket, m.buildS3Path(storePath), expires, reqParams)
		return u, convertS3Err(err)
	}
	u, err := m.client.PresignedGetObject(m.ctx, m.bucket, m.buildS3Path(storePath), expires, reqParams)
	return u, convertS3Err(err)
}

// IterateObjects iterates across the objects in the S3 storage
func (m *S3Storage) IterateObjects(dirName string, fn func(path string, obj Object) error) error {
	opts := minio.GetObjectOptions{}
	// FIXME: this loop is not right and causes resource leaking, see the comment of ListObjects
	for mObjInfo := range m.client.ListObjects(m.ctx, m.bucket, minio.ListObjectsOptions{
		Prefix:    m.buildS3DirPrefix(dirName),
		Recursive: true,
	}) {
		object, err := m.client.GetObject(m.ctx, m.bucket, mObjInfo.Key, opts)
		if err != nil {
			return convertS3Err(err)
		}
		if err := func(object *minio.Object, fn func(path string, obj Object) error) error {
			defer object.Close()
			return fn(strings.TrimPrefix(mObjInfo.Key, m.basePath), &s3Object{object})
		}(object, fn); err != nil {
			return convertS3Err(err)
		}
	}
	return nil
}

func init() {
	RegisterStorageType(setting.S3StorageType, NewS3Storage)
}
