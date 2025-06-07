package common

import (
	"net/url"
	"time"
)

type BackupSettings struct {
	BaseUrl       *url.URL
	EncryptionKey string
	DownloadTo    *url.URL
	Timeout       time.Duration
}

type BackupFile struct {
	Name     string
	Contents []byte
}

type RequestResult struct {
	File BackupFile

	Err error
}
type UploadResult struct {
	Err error
}
