package main

import (
	"context"
	"fmt"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"log"
	"net/url"
	"strings"
	"sync"
	"tiktocker/internal/backup"
	"tiktocker/internal/storage"
	"tiktocker/internal/util"
	"time"
)

type Config struct {
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
	config, err := setupConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
		return
	}
	util.Setup(config.Log.Level)
	util.Log.Infof("Mikrotik Backup starting")

	var wg sync.WaitGroup
	targets := createTargets(config)

	util.Log.Infof("found %d Mikrotik devices to backup (out of: %d)", len(targets), len(config.Mikrotiks))

	for _, settings := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), settings.Timeout)
		defer cancel()

		wg.Add(1)

		go func() {
			defer wg.Done()
			mainChannel := make(chan *backup.RequestResult) //experiment with moving channel out of this gorouteine
			defer close(mainChannel)

			go backup.MikrotikBackup(&ctx, settings, mainChannel)
			backupFile, err := util.WaitForResult(ctx, mainChannel)
			if err != nil || backupFile.Err != nil {
				util.Log.Errorf("failed to backup Mikrotik %s: %v", settings.BaseUrl.Host, backupFile.Err)
				return
			}

			util.Log.Infof("backup file downloaded from %s: %s (%d bytes)", settings.BaseUrl.Host, backupFile.File.Name, len(backupFile.File.Contents))

			go storage.UploadFile(settings, &backupFile.File, mainChannel)
			_, err = util.WaitForResult(ctx, mainChannel)
			if err != nil {
				util.Log.Errorf("backup file upload failure")
			}
			util.Log.Infof("Mikrotik %s backup completed successfully", settings.BaseUrl.Host)
		}()
	}

	wg.Wait()
}

func createTargets(config *Config) []*backup.BackupSettings {
	targets := make([]*backup.BackupSettings, 0, len(config.Mikrotiks))

	for _, target := range config.Mikrotiks {
		u, err := util.CreateUrl(target.Host, target.Username, target.Password)
		if err != nil {
			util.Log.Errorf("failed to create URL for Mikrotik %s: %v", target.Host, err)
			continue
		}
		t, err := url.Parse(target.DownloadTo)
		if err != nil {
			util.Log.Errorf("failed to parse download URL for Mikrotik %s: %v", target.Host, err)
			continue
		}
		timeout := target.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second // Default timeout if not set
		}
		targets = append(targets, &backup.BackupSettings{
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
