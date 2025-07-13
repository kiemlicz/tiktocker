package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"tiktocker/internal/common"
)

func StoreFile(
	destDir string,
	file *common.BackupFile,
	mainComms chan *common.RequestResult,
) {
	destPath := filepath.Join(destDir, file.Name)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		common.Log.Errorf("failed to create directory: %v", err)
		mainComms <- &common.RequestResult{Err: fmt.Errorf("failed to create directory: %w", err)}
		return
	}
	if err := os.WriteFile(destPath, file.Contents, 0644); err != nil {
		common.Log.Errorf("Failed to save backup to file: %v", err)
		mainComms <- &common.RequestResult{Err: fmt.Errorf("failed to save backup: %w", err)}
		return
	}
	common.Log.Infof("backup saved to %s", destPath)
	mainComms <- &common.RequestResult{}
}

func UploadFile(
	ctx context.Context,
	s3Client *common.S3Connector,
	file *common.BackupFile,
	metadata *map[string]string,
	mainComms chan *common.RequestResult,
) {
	err := s3Client.UploadFile(ctx, file, metadata)
	if err != nil {
		mainComms <- &common.RequestResult{Err: fmt.Errorf("s3 bucket: %s upload failure: %w", s3Client.Bucket, err)}
		return
	}
	common.Log.Infof("file: %s uploaded to S3", file.Name)
	mainComms <- &common.RequestResult{Err: nil}
}
