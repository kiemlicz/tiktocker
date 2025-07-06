package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"os"
	"path/filepath"
	"tiktocker/internal/common"

	"crypto/sha256"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func StoreFile(
	destDir string,
	file *common.BackupFile,
	mainComms chan *common.UploadResult,
) {
	destPath := filepath.Join(destDir, file.Name)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		common.Log.Errorf("failed to create directory: %v", err)
		mainComms <- &common.UploadResult{Err: fmt.Errorf("failed to create directory: %w", err)}
		return
	}
	if err := os.WriteFile(destPath, file.Contents, 0644); err != nil {
		common.Log.Errorf("Failed to save backup to file: %v", err)
		mainComms <- &common.UploadResult{Err: fmt.Errorf("failed to save backup: %w", err)}
		return
	}
	common.Log.Infof("backup saved to %s", destPath)
	mainComms <- &common.UploadResult{}
}

func UploadFile(
	ctx context.Context,
	s3Client *common.S3Connector,
	file *common.BackupFile,
	mainComms chan *common.UploadResult,
) {
	bucket := s3Client.Bucket
	bucketPath := s3Client.Prefix
	if !strings.HasSuffix(bucketPath, file.Name) {
		bucketPath = filepath.Join(bucketPath, file.Name)
	}

	// Compute local checksum
	localSum := sha256.Sum256(file.Contents)
	localSumBase64 := base64.StdEncoding.EncodeToString(localSum[:])

	// Check remote object's checksum (if exists)
	head, err := s3Client.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(bucketPath),
		ChecksumMode: types.ChecksumModeEnabled, //otherwise won't fetch the checksum
	})
	if err == nil && head.ChecksumSHA256 != nil {
		remoteSumBase64 := head.ChecksumSHA256
		if localSumBase64 == *remoteSumBase64 {
			common.Log.Infof("s3 object %s/%s already up-to-date (checksum match)", bucket, bucketPath)
			mainComms <- &common.UploadResult{Err: nil}
			return
		}
		// mikrotik backup almost always differs...
	} else {
		common.Log.Debugf("s3 object %s/%s does not exist or error occurred: %v, proceeding", bucket, bucketPath, err)
	}

	uploader := manager.NewUploader(s3Client.Client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String(bucketPath),
		Body:              bytes.NewReader(file.Contents),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(localSumBase64),
	})

	if err != nil {
		mainComms <- &common.UploadResult{Err: fmt.Errorf("s3 bucket: %s upload failure: %w", bucket, err)}
		return
	}

	common.Log.Infof("backup uploaded to S3")
	mainComms <- &common.UploadResult{Err: nil}
}
