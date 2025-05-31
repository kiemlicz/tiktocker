package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"tiktocker/internal/backup"
	"tiktocker/internal/util"
)

func UploadFile(settings *backup.BackupSettings, file *backup.BackupFile, deviceComms chan *backup.RequestResult) {
	switch settings.DownloadTo.Scheme {
	case "file":
		destDir := settings.DownloadTo.Path
		destPath := filepath.Join(destDir, file.Name)
		if err := os.WriteFile(destPath, file.Contents, 0644); err != nil {
			util.Log.Errorf("Failed to save backup to file: %v", err)
			deviceComms <- &backup.RequestResult{Err: fmt.Errorf("failed to save backup: %w", err)}
			return
		}
		util.Log.Infof("Backup saved to %s", destPath)
		deviceComms <- &backup.RequestResult{Err: nil}
	case "s3":
		//todo don't ovewrrite if checksum matches
		util.Log.Warnf("not yet implemented: S3 upload")
	default:
		util.Log.Warnf("Unsupported upload scheme: %s", settings.DownloadTo.Scheme)
	}
}
