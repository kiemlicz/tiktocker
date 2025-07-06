package common

import (
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/url"
	"time"
)

type BackupSettings struct {
	BaseUrl       *url.URL
	EncryptionKey string
	Timeout       time.Duration
}

type BackupFile struct {
	Name     string
	Contents []byte
}

type S3Connector struct {
	Client *s3.Client
	Bucket string
	Prefix string
}

type RequestResult struct {
	File BackupFile

	Err error
}
type UploadResult struct {
	Err error
}
