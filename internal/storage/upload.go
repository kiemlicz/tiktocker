package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"tiktocker/internal/common"

	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func UploadFile(ctx context.Context, s3Client *s3.Client, settings *common.BackupSettings, file *common.BackupFile, mainComms chan *common.UploadResult) {
	switch settings.DownloadTo.Scheme {
	case "file":
		destDir := settings.DownloadTo.Path
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

	case "s3", "": // meh this alternative
		err := uploadToS3(ctx, s3Client, settings.DownloadTo, file)
		if err != nil {
			common.Log.Errorf("failed to upload to S3: %v", err)
			mainComms <- &common.UploadResult{Err: fmt.Errorf("failed to upload to S3: %w", err)}
			return
		}
		common.Log.Infof("backup uploaded to S3")

	default:
		common.Log.Warnf("unsupported upload scheme: %s", settings.DownloadTo.Scheme)
		mainComms <- &common.UploadResult{Err: fmt.Errorf("unsupported upload scheme: %s", settings.DownloadTo.Scheme)}
		return
	}

	mainComms <- &common.UploadResult{Err: nil}
}

func uploadToS3(ctx context.Context, s3Client *s3.Client, DownloadTo *url.URL, file *common.BackupFile) error {
	// Parse bucket and bucketPath from settings.DownloadTo
	// s3://host/bucket/prefix/filename
	parts := strings.SplitN(strings.TrimPrefix(DownloadTo.Path, "/"), "/", 2)
	if len(parts) < 2 {
		return fmt.Errorf("invalid S3 scheme's path: %s, must have at least two segments", DownloadTo.String())
	}
	bucket := parts[0]
	bucketPath := parts[1]
	if !strings.HasSuffix(bucketPath, file.Name) {
		bucketPath = filepath.Join(bucketPath, file.Name)
	}

	// Compute local checksum
	localSum := sha256.Sum256(file.Contents)
	localSumHex := hex.EncodeToString(localSum[:])

	// Check remote object's checksum (if exists)
	head, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(bucketPath),
	})
	if err == nil {
		remoteSum := head.Metadata["Sha256sum"]
		if remoteSum == localSumHex {
			common.Log.Infof("S3 object %s/%s already up-to-date (checksum match)", bucket, bucketPath)
			return nil
		}
	} else {
		common.Log.Debugf("S3 object %s/%s does not exist or error occurred: %v, proceeding", bucket, bucketPath, err)
	}

	// Upload with checksum in metadata
	uploader := manager.NewUploader(s3Client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(bucketPath),
		Body:     bytes.NewReader(file.Contents),
		Metadata: map[string]string{"Sha256sum": localSumHex},
	})
	if err != nil {
		return fmt.Errorf("bucket: %s upload failure: %w", bucket, err)
	}
	return nil
}
