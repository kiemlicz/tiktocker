package common

import (
	"bytes"
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	Sha256WithoutFirstLine = "tiktockersha256"
)

type BackupSettings struct {
	BaseUrl       *url.URL
	EncryptionKey string
	Timeout       time.Duration
	Metadata      map[string]string
}

type BackupFile struct {
	Name                           string
	Contents                       []byte
	ComputedSha256                 string // base64 encoded sha256 checksum of the file contents
	ComputedSha256WithoutFirstLine string // base64 encoded sha256 checksum of the file contents without the first line
}

type S3Connector struct {
	Client *s3.Client
	Bucket string
	Prefix string
}

// GetObjectSha256 returns modified sha256 to detect Mikrotik config changes, modified == sha256 based on full file without first line that contains date
func (c *S3Connector) GetObjectSha256(ctx context.Context, fileName string) *string {
	bucketPath := c.Prefix
	if !strings.HasSuffix(bucketPath, fileName) {
		bucketPath = filepath.Join(bucketPath, fileName)
	}
	head, err := c.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(c.Bucket),
		Key:          aws.String(bucketPath),
		ChecksumMode: types.ChecksumModeEnabled, //otherwise won't fetch the checksum
	})

	// checksum might be computed with first line omitted hence it is kept in different field
	if err == nil && head.Metadata != nil {
		val := head.Metadata[Sha256WithoutFirstLine]
		return &val
	}
	return nil
}

func (c *S3Connector) UploadFile(ctx context.Context, file *BackupFile, metadata *map[string]string) error {
	bucketPath := c.Prefix
	if !strings.HasSuffix(bucketPath, file.Name) {
		bucketPath = filepath.Join(bucketPath, file.Name)
	}

	m := metadata

	if file.ComputedSha256WithoutFirstLine != "" {
		modifiedMetadata := make(map[string]string, len(*metadata)+1)
		for k, v := range *metadata {
			modifiedMetadata[k] = v
		}
		modifiedMetadata[Sha256WithoutFirstLine] = file.ComputedSha256WithoutFirstLine
		m = &modifiedMetadata
	}

	uploader := manager.NewUploader(c.Client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(c.Bucket),
		Key:               aws.String(bucketPath),
		Body:              bytes.NewReader(file.Contents),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(file.ComputedSha256),
		Metadata:          *m,
	})
	return err
}

type RequestResult struct {
	MikrotikIdentity     string
	File                 BackupFile
	ExistingConfigSha256 *string // base64 encoded sha256 checksum of the remote file

	Err error
}

func (r *RequestResult) ShouldPerformNewBackup() bool {
	return r.ExistingConfigSha256 == nil || *r.ExistingConfigSha256 != r.File.ComputedSha256WithoutFirstLine
}
