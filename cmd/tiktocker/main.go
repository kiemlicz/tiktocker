package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"log"
	"net/url"
	"strings"
	"sync"
	"tiktocker/internal/backup"
	"tiktocker/internal/common"
	"tiktocker/internal/storage"
	"time"
)

type Config struct {
	S3 struct {
		Host      string `mapstructure:"host"`
		AccessKey string `mapstructure:"accessKey"`
		SecretKey string `mapstructure:"secretKey"`
		Region    string `mapstructure:"region"`
		// fixme add full path here
	} `mapstructure:"s3"`

	Log struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"log"`

	Mikrotiks []struct {
		Host          string        `mapstructure:"host"`
		Username      string        `mapstructure:"username"`
		Password      string        `mapstructure:"password"`
		EncryptionKey string        `mapstructure:"encryptionKey"`
		DownloadTo    string        `mapstructure:"downloadTo"`
		Timeout       time.Duration `mapstructure:"timeout"`
	} `mapstructure:"mikrotiks"`
}

func main() {
	ttConfig, err := setupConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
		return
	}
	common.Setup(ttConfig.Log.Level)
	common.Log.Infof("Mikrotik Backup starting")

	mainCtx := context.Background()
	var wg sync.WaitGroup
	targets := createTargets(ttConfig)
	//fixme validate that any target contains s3, otherwise don't require s3client
	s3Client, err := createS3Client(ttConfig)
	if err != nil {
		common.Log.Fatalf("failed to create S3 client: %v", err)
		return
	}

	common.Log.Infof("found %d Mikrotik devices to backup (out of: %d)", len(targets), len(ttConfig.Mikrotiks))

	for _, settings := range targets {
		ctx, cancel := context.WithTimeout(mainCtx, settings.Timeout)
		defer cancel()

		wg.Add(1)

		go func() {
			defer wg.Done()
			mainBackupChannel := make(chan *common.RequestResult) //experiment with moving channel out of this gorouteine
			defer close(mainBackupChannel)

			go backup.MikrotikBackup(ctx, settings, mainBackupChannel)
			backupFile, err := common.WaitForResult(ctx, mainBackupChannel)
			if err != nil {
				mainBackupChannel <- &common.RequestResult{
					Err: fmt.Errorf("ctx cancelled, backup failure: %v", err),
				}
				return
			}
			if backupFile.Err != nil {
				common.Log.Errorf("failed to backup Mikrotik %s: %v", settings.BaseUrl.Host, backupFile.Err)
				return
			}

			common.Log.Infof("backup file downloaded from %s: %s (%d bytes)", settings.BaseUrl.Host, backupFile.File.Name, len(backupFile.File.Contents))

			mainUploadChannel := make(chan *common.UploadResult) //experiment with moving channel out of this gorouteine
			defer close(mainUploadChannel)
			go storage.UploadFile(ctx, s3Client, settings, &backupFile.File, mainUploadChannel)
			uploadResult, err := common.WaitForResult(ctx, mainUploadChannel)
			if err != nil {
				mainUploadChannel <- &common.UploadResult{
					Err: fmt.Errorf("ctx cancelled, backup failure: %v", err),
				}
				return
			}
			if uploadResult.Err != nil {
				common.Log.Errorf("backup file upload failure")
				return
			}
			common.Log.Infof("Mikrotik %s backup completed successfully", settings.BaseUrl.Host)
		}()
	}

	wg.Wait()
}

func createS3Client(c *Config) (*s3.Client, error) {
	s3Region := c.S3.Region
	s3AccessKey := c.S3.AccessKey
	s3SecretKey := c.S3.SecretKey
	s3Host := c.S3.Host

	cfg := aws.Config{
		Region:       s3Region,
		BaseEndpoint: &s3Host,
		Credentials:  credentials.NewStaticCredentialsProvider(s3AccessKey, s3SecretKey, ""),
	}

	client := s3.NewFromConfig(
		cfg,
		func(o *s3.Options) {
			o.UsePathStyle = true // Use path-style URLs for S3
		},
	)
	return client, nil
}

func createTargets(config *Config) []*common.BackupSettings {
	targets := make([]*common.BackupSettings, 0, len(config.Mikrotiks))

	for _, target := range config.Mikrotiks {
		u, err := common.CreateUrl(target.Host, target.Username, target.Password)
		if err != nil {
			common.Log.Errorf("failed to create URL for Mikrotik %s: %v", target.Host, err)
			continue
		}
		t, err := url.Parse(target.DownloadTo)
		if err != nil {
			common.Log.Errorf("failed to parse download URL for Mikrotik %s: %v", target.Host, err)
			continue
		}
		timeout := target.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second // Default timeout if not set
		}

		targets = append(targets, &common.BackupSettings{
			BaseUrl:       u,
			EncryptionKey: target.EncryptionKey,
			DownloadTo:    t,
			Timeout:       timeout,
		})
	}
	return targets
}

func setupConfig() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("/etc/tiktocker")
	v.AddConfigPath(".")
	v.SetEnvPrefix("TT")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	pflag.String("log.level", "", "log level (overrides yaml file)")
	pflag.Parse()
	_ = v.BindPFlags(pflag.CommandLine)

	err := v.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %s", err))
	}

	var config *Config
	err = v.Unmarshal(&config)
	if err != nil {
		log.Fatalf("Unable to decode into struct, %v", err)
		return config, err
	}

	return config, nil
}
