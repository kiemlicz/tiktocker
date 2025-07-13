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
	"net/http"
	"os"
	"strings"
	"sync"
	"tiktocker/internal/backup"
	"tiktocker/internal/common"
	"tiktocker/internal/storage"
	"time"
)

type Config struct {
	Directory string `mapstructure:"directory"` // directory to store backups, if empty - uses S3

	S3 struct {
		Host         string `mapstructure:"host"`
		AccessKey    string `mapstructure:"accessKey"`
		SecretKey    string `mapstructure:"secretKey"`
		Region       string `mapstructure:"region"`
		Path         string `mapstructure:"path"`         // bucket/pathPrefix
		UsePathStyle bool   `mapstructure:"usePathStyle"` // ex Minio uses path style, AWS S3 does not
	} `mapstructure:"s3"`

	Log struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"log"`

	Mikrotiks []struct {
		Host          string            `mapstructure:"host"`
		Username      string            `mapstructure:"username"`
		Password      string            `mapstructure:"password"`
		EncryptionKey string            `mapstructure:"encryptionKey"`
		Timeout       time.Duration     `mapstructure:"timeout"`
		Metadata      map[string]string `mapstructure:"metadata"`
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

	var s3Connector *common.S3Connector
	var wg sync.WaitGroup
	targets := createTargets(ttConfig)

	localPathDownload := ttConfig.Directory
	if localPathDownload == "" {
		s3Connector, err = createS3Client(ttConfig)
		if err != nil {
			common.Log.Fatalf("failed to create S3 client: %v", err)
			return
		}
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
			client := &http.Client{
				Timeout: 10 * time.Second,
			}

			go backup.MikrotikConfigExport(ctx, settings, client, mainBackupChannel)
			configFileResult := common.WaitForResult(ctx, mainBackupChannel)
			if configFileResult.Err != nil {
				common.Log.Errorf("failed to download Mikrotik %s config: %v", settings.BaseUrl.Host, configFileResult.Err)
				return
			}

			if s3Connector != nil {
				go func() {
					mainBackupChannel <- &common.RequestResult{
						MikrotikIdentity:     configFileResult.MikrotikIdentity,
						ExistingConfigSha256: s3Connector.GetObjectSha256(ctx, configFileResult.File.Name),
					}
				}()
				s3MetadataResult := common.WaitForResult(ctx, mainBackupChannel)
				configFileResult.ExistingConfigSha256 = s3MetadataResult.ExistingConfigSha256
			}

			if configFileResult.ShouldPerformNewBackup() {
				common.Log.Infof("Mikrotik (host: %s, identity: %s) config has changed, proceeding with backup", settings.BaseUrl.Host, configFileResult.MikrotikIdentity)

				go backup.MikrotikBackup(ctx, configFileResult.MikrotikIdentity, settings, client, mainBackupChannel)
				backupFileResult := common.WaitForResult(ctx, mainBackupChannel)
				if backupFileResult.Err != nil {
					common.Log.Errorf("failed to backup Mikrotik %s: %v", settings.BaseUrl.Host, backupFileResult.Err)
					return
				}

				common.Log.Infof("backup file downloaded from %s: %s (%d bytes)", settings.BaseUrl.Host, backupFileResult.File.Name, len(backupFileResult.File.Contents))
				if s3Connector != nil {
					go storage.UploadFile(ctx, s3Connector, &configFileResult.File, &settings.Metadata, mainBackupChannel)
					configFileUploadResult := common.WaitForResult(ctx, mainBackupChannel)
					if configFileUploadResult.Err != nil {
						common.Log.Errorf("config file upload failure: %v", configFileUploadResult.Err)
						return
					}

					go storage.UploadFile(ctx, s3Connector, &backupFileResult.File, &settings.Metadata, mainBackupChannel)
					backupUploadResult := common.WaitForResult(ctx, mainBackupChannel)
					if backupUploadResult.Err != nil {
						common.Log.Errorf("backup file upload failure: %v", backupUploadResult.Err)
						return
					}
				} else {
					go storage.StoreFile(localPathDownload, &configFileResult.File, mainBackupChannel)
					storeResult := common.WaitForResult(ctx, mainBackupChannel)
					if storeResult.Err != nil {
						common.Log.Errorf("config file upload failure: %v", storeResult.Err)
						return
					}

					go storage.StoreFile(localPathDownload, &backupFileResult.File, mainBackupChannel)
					storeResult = common.WaitForResult(ctx, mainBackupChannel)
					if storeResult.Err != nil {
						common.Log.Errorf("backup file upload failure: %v", storeResult.Err)
						return
					}
					common.Log.Infof("Mikrotik %s backup completed successfully", settings.BaseUrl.Host)
				}
			} else {
				common.Log.Infof("Mikrotik (host: %s, identity: %s) config has not changed, skipping backup", settings.BaseUrl.Host, configFileResult.MikrotikIdentity)
				return
			}
		}()
	}

	wg.Wait()
}

func createS3Client(c *Config) (*common.S3Connector, error) {
	s3Region := c.S3.Region
	s3AccessKey := c.S3.AccessKey
	s3SecretKey := c.S3.SecretKey
	s3Host := c.S3.Host
	s3BucketPrefix := c.S3.Path
	s3PathStyle := c.S3.UsePathStyle

	bucketPrefix := strings.SplitN(strings.TrimPrefix(s3BucketPrefix, "/"), "/", 2)
	if (len(bucketPrefix) < 2) || (bucketPrefix[0] == "" || bucketPrefix[1] == "") {
		return nil, fmt.Errorf("invalid S3 path: %s, must be in format bucket/prefix", s3BucketPrefix)
	}
	bucket := bucketPrefix[0]
	bucketPath := bucketPrefix[1]

	cfg := aws.Config{
		Region:       s3Region,
		BaseEndpoint: &s3Host,
		Credentials:  credentials.NewStaticCredentialsProvider(s3AccessKey, s3SecretKey, ""),
	}

	connector := &common.S3Connector{
		Client: s3.NewFromConfig(
			cfg,
			func(o *s3.Options) {
				o.UsePathStyle = s3PathStyle
			},
		),
		Bucket: bucket,
		Prefix: bucketPath,
	}
	return connector, nil
}

func createTargets(config *Config) []*common.BackupSettings {
	targets := make([]*common.BackupSettings, 0, len(config.Mikrotiks))

	for _, target := range config.Mikrotiks {
		u, err := common.CreateUrl(target.Host, target.Username, target.Password)
		if err != nil {
			common.Log.Errorf("failed to create URL for Mikrotik %s: %v", target.Host, err)
			continue
		}
		timeout := target.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second // Default timeout if not set
		}

		targets = append(targets, &common.BackupSettings{
			BaseUrl:       u,
			EncryptionKey: target.EncryptionKey,
			Timeout:       timeout,
			Metadata:      target.Metadata,
		})
	}
	return targets
}

func setupConfig() (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("TT")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetConfigFile("config.yaml") // default config file full path, not adding paths as they pick single file

	pflag.String("log.level", "", "log level (overrides yaml file)")
	pflag.Parse()
	_ = v.BindPFlags(pflag.CommandLine)

	if err := v.ReadInConfig(); err != nil {
		panic(fmt.Errorf("error reading config file, %s", err))
	}

	loader := func(configFullPath string) {
		if _, err := os.Stat(configFullPath); err == nil {
			v.SetConfigFile(configFullPath)
			if err := v.MergeInConfig(); err != nil {
				panic(fmt.Errorf("error merging config file, %s", err))
			}
		}
	}

	loader("/etc/tiktocker/config.yaml")
	loader(".local/config.yaml")

	var config *Config
	err := v.Unmarshal(&config)
	if err != nil {
		log.Fatalf("Unable to decode into struct, %v", err)
		return config, err
	}

	return config, nil
}
