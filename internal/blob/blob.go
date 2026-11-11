// Package blob 封装 MinIO 对象存储：存文档原件。
package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client 是 MinIO 的薄封装。
type Client struct {
	c      *minio.Client
	bucket string
}

// New 建立连接（不校验连通性）。
func New(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Client, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: init minio: %w", err)
	}
	return &Client{c: c, bucket: bucket}, nil
}

// EnsureBucket 幂等创建 bucket。
func (b *Client) EnsureBucket(ctx context.Context) error {
	exists, err := b.c.BucketExists(ctx, b.bucket)
	if err != nil {
		return fmt.Errorf("blob: check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := b.c.MakeBucket(ctx, b.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("blob: make bucket: %w", err)
	}
	return nil
}

// Put 上传一份对象。
func (b *Client) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := b.c.PutObject(ctx, b.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("blob: put %s: %w", key, err)
	}
	return nil
}

// Get 下载一份对象。
func (b *Client) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := b.c.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blob: get %s: %w", key, err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("blob: read %s: %w", key, err)
	}
	return data, nil
}

// Healthy 探活。
func (b *Client) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := b.c.BucketExists(ctx, b.bucket)
	return err
}
