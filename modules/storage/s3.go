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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var _ ObjectStorage = &S3Storage{}

// S3Storage is an S3-compatible object storage backed by the permissive
// aws-sdk-go-v2 client (Apache-2.0) — no minio lineage. Against the Hanzo S3
// substrate (hanzoai/s3, SeaweedFS) it uses path-style addressing.
type S3Storage struct {
	cfg      *setting.S3StorageConfig
	ctx      context.Context
	client   *s3.Client
	presign  *s3.PresignClient
	uploader *manager.Uploader
	bucket   string
	basePath string
}

// convertS3Err maps the S3/smithy API errors Gitea's storage layer cares about
// (missing key, denied) onto the standard os errors it checks for.
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

	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return wrapErr(os.ErrNotExist)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return wrapErr(os.ErrNotExist)
		case "AccessDenied":
			return wrapErr(os.ErrPermission)
		}
	}
	return wrapErr(err)
}

// NewS3Storage returns an S3 storage.
func NewS3Storage(ctx context.Context, cfg *setting.Storage) (ObjectStorage, error) {
	config := cfg.S3Config
	log.Info("Creating S3 storage at %s:%s with base path %s", config.Endpoint, config.Bucket, config.BasePath)
	if config.ChecksumAlgorithm != "" && config.ChecksumAlgorithm != "default" && config.ChecksumAlgorithm != "md5" {
		return nil, fmt.Errorf("invalid S3 checksum algorithm: %s", config.ChecksumAlgorithm)
	}

	scheme := "http://"
	if config.UseSSL {
		scheme = "https://"
	}
	endpoint := config.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = scheme + endpoint
	}
	region := config.Location
	if region == "" {
		region = "us-east-1"
	}

	makeErrMsg := func(hint string) string {
		return fmt.Sprintf("ObjectStorage.%s: endpoint=%s, location=%s, bucket=%s", hint, config.Endpoint, config.Location, config.Bucket)
	}

	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: config.InsecureSkipVerify}, //nolint:gosec // admin opt-in
	}}

	opts := s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  buildS3Credentials(config),
		HTTPClient:   httpClient,
		// SeaweedFS (and most self-hosted S3) require path-style, not the AWS
		// virtual-hosted (bucket-as-subdomain) style. "dns" opts into
		// virtual-hosted; anything else stays path-style.
		UsePathStyle: config.BucketLookUpType != "dns",
	}
	client := s3.New(opts)

	// Ensure the bucket exists (create it once on first boot, like the prior driver).
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(config.Bucket)}); err != nil {
		var nf *types.NotFound
		var apiErr smithy.APIError
		is404 := errors.As(err, &nf) || (errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "404" || apiErr.ErrorCode() == "NoSuchBucket"))
		if !is404 {
			return nil, convertS3Err(err, makeErrMsg("HeadBucket"))
		}
		if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(config.Bucket)}); err != nil {
			return nil, convertS3Err(err, makeErrMsg("CreateBucket"))
		}
	}

	return &S3Storage{
		cfg:      &config,
		ctx:      ctx,
		client:   client,
		presign:  s3.NewPresignClient(client),
		uploader: manager.NewUploader(client),
		bucket:   config.Bucket,
		basePath: config.BasePath,
	}, nil
}

// buildS3Credentials returns a credentials provider: static keys when configured
// (the fleet path — creds from the s3-credentials secret), else anonymous.
func buildS3Credentials(config setting.S3StorageConfig) aws.CredentialsProvider {
	if config.AccessKeyID != "" {
		return credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, "")
	}
	return aws.AnonymousCredentials{}
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

// s3Object is a seekable reader over an S3 object. aws-sdk-go-v2 returns a plain
// stream, so Seek is implemented by (re)issuing a ranged GetObject at the new
// offset. The interface requires io.Seeker (HTTP range serving of LFS/attachments);
// sequential reads reuse the open stream, a Seek invalidates it, and the next Read
// re-opens at the new offset.
type s3Object struct {
	ctx    context.Context
	client *s3.Client
	bucket string
	key    string
	size   int64 // -1 until known (from a ranged GET or Stat)
	offset int64
	body   io.ReadCloser
}

// reopen ensures body is an open stream positioned at offset. A live stream is
// left in place (sequential read); it is only (re)issued when nil (fresh open or
// post-Seek).
func (o *s3Object) reopen() error {
	if o.body != nil {
		return nil
	}
	rng := fmt.Sprintf("bytes=%d-", o.offset)
	out, err := o.client.GetObject(o.ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket), Key: aws.String(o.key), Range: aws.String(rng),
	})
	if err != nil {
		return convertS3Err(err)
	}
	o.body = out.Body
	if o.size < 0 && out.ContentLength != nil {
		o.size = o.offset + *out.ContentLength // ContentLength is the remaining bytes from offset
	}
	return nil
}

func (o *s3Object) Read(p []byte) (int, error) {
	if err := o.reopen(); err != nil {
		return 0, err
	}
	n, err := o.body.Read(p)
	o.offset += int64(n)
	return n, err
}

func (o *s3Object) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = o.offset + offset
	case io.SeekEnd:
		if o.size < 0 {
			st, err := o.client.HeadObject(o.ctx, &s3.HeadObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(o.key)})
			if err != nil {
				return 0, convertS3Err(err)
			}
			if st.ContentLength != nil {
				o.size = *st.ContentLength
			}
		}
		abs = o.size + offset
	default:
		return 0, fmt.Errorf("s3 object: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("s3 object: negative position %d", abs)
	}
	if abs != o.offset {
		o.offset = abs
		if o.body != nil {
			_ = o.body.Close()
			o.body = nil
		}
	}
	return abs, nil
}

func (o *s3Object) Close() error {
	if o.body != nil {
		err := o.body.Close()
		o.body = nil
		return err
	}
	return nil
}

func (o *s3Object) Stat() (os.FileInfo, error) {
	st, err := o.client.HeadObject(o.ctx, &s3.HeadObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(o.key)})
	if err != nil {
		return nil, convertS3Err(err)
	}
	fi := &s3FileInfo{key: o.key}
	if st.ContentLength != nil {
		fi.size = *st.ContentLength
	}
	if st.LastModified != nil {
		fi.modTime = *st.LastModified
	}
	return fi, nil
}

// Open opens a file.
func (m *S3Storage) Open(p string) (Object, error) {
	key := m.buildS3Path(p)
	// Fail fast with a not-exist error the callers check, rather than at first Read.
	if _, err := m.client.HeadObject(m.ctx, &s3.HeadObjectInput{Bucket: aws.String(m.bucket), Key: aws.String(key)}); err != nil {
		return nil, convertS3Err(err)
	}
	return &s3Object{ctx: m.ctx, client: m.client, bucket: m.bucket, key: key, size: -1}, nil
}

// Save saves a file to S3. The uploader streams (multipart as needed), so an
// unknown size (-1) is supported.
func (m *S3Storage) Save(p string, r io.Reader, size int64) (int64, error) {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(m.bucket),
		Key:         aws.String(m.buildS3Path(p)),
		Body:        r,
		ContentType: aws.String("application/octet-stream"),
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if _, err := m.uploader.Upload(m.ctx, in); err != nil {
		return 0, convertS3Err(err)
	}
	if size >= 0 {
		return size, nil
	}
	st, err := m.client.HeadObject(m.ctx, &s3.HeadObjectInput{Bucket: aws.String(m.bucket), Key: in.Key})
	if err != nil {
		return 0, convertS3Err(err)
	}
	if st.ContentLength != nil {
		return *st.ContentLength, nil
	}
	return 0, nil
}

type s3FileInfo struct {
	key     string
	size    int64
	modTime time.Time
}

func (m s3FileInfo) Name() string       { return path.Base(m.key) }
func (m s3FileInfo) Size() int64        { return m.size }
func (m s3FileInfo) ModTime() time.Time { return m.modTime }
func (m s3FileInfo) IsDir() bool        { return strings.HasSuffix(m.key, "/") }
func (m s3FileInfo) Mode() os.FileMode  { return os.ModePerm }
func (m s3FileInfo) Sys() any           { return nil }

// Stat returns the stat information of the object.
func (m *S3Storage) Stat(p string) (os.FileInfo, error) {
	key := m.buildS3Path(p)
	st, err := m.client.HeadObject(m.ctx, &s3.HeadObjectInput{Bucket: aws.String(m.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, convertS3Err(err)
	}
	fi := &s3FileInfo{key: key}
	if st.ContentLength != nil {
		fi.size = *st.ContentLength
	}
	if st.LastModified != nil {
		fi.modTime = *st.LastModified
	}
	return fi, nil
}

// Delete deletes a file.
func (m *S3Storage) Delete(p string) error {
	_, err := m.client.DeleteObject(m.ctx, &s3.DeleteObjectInput{Bucket: aws.String(m.bucket), Key: aws.String(m.buildS3Path(p))})
	return convertS3Err(err)
}

// ServeDirectURL returns a presigned URL so the client fetches the object
// directly from S3.
func (m *S3Storage) ServeDirectURL(storePath, name, method string, opt *ServeDirectOptions) (*url.URL, error) {
	key := m.buildS3Path(storePath)
	param := prepareServeDirectOptions(opt, name)
	expires := func(o *s3.PresignOptions) { o.Expires = 5 * time.Minute }

	if method == http.MethodHead {
		req, err := m.presign.PresignHeadObject(m.ctx, &s3.HeadObjectInput{Bucket: aws.String(m.bucket), Key: aws.String(key)}, expires)
		if err != nil {
			return nil, convertS3Err(err)
		}
		return url.Parse(req.URL)
	}
	in := &s3.GetObjectInput{Bucket: aws.String(m.bucket), Key: aws.String(key)}
	if param.ContentType != "" {
		in.ResponseContentType = aws.String(param.ContentType)
	}
	if param.ContentDisposition != "" {
		in.ResponseContentDisposition = aws.String(param.ContentDisposition)
	}
	req, err := m.presign.PresignGetObject(m.ctx, in, expires)
	if err != nil {
		return nil, convertS3Err(err)
	}
	return url.Parse(req.URL)
}

// IterateObjects iterates across the objects in the S3 storage.
func (m *S3Storage) IterateObjects(dirName string, fn func(path string, obj Object) error) error {
	prefix := m.buildS3DirPrefix(dirName)
	paginator := s3.NewListObjectsV2Paginator(m.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(m.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(m.ctx)
		if err != nil {
			return convertS3Err(err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			obj := &s3Object{ctx: m.ctx, client: m.client, bucket: m.bucket, key: key, size: -1}
			if o.Size != nil {
				obj.size = *o.Size
			}
			err := func() error {
				defer obj.Close()
				return fn(strings.TrimPrefix(key, m.basePath), obj)
			}()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func init() {
	RegisterStorageType(setting.S3StorageType, NewS3Storage)
}
