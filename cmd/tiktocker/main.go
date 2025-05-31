package main

import (
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
	Mikrotiks map[string]struct {
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
	util.Setup()
	util.Log.Infof("Mikrotik Backup starting")

	var wg sync.WaitGroup
	targets := createTargets(config)

	util.Log.Infof("found %d Mikrotik devices to backup (out of: %d)", len(targets), len(config.Mikrotiks))

	for _, settings := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mainChannel := make(chan *backup.RequestResult) //experiment with moving channel out of this gorouteine
			//defer close(mainChannel)

			go backup.MikrotikBackup(settings, mainChannel)

			var backupFile backup.RequestResult

			select {
			case backupFile := <-mainChannel:
				if backupFile.Err != nil {
					util.Log.Errorf("failed to backup Mikrotik %s: %v", settings.BaseUrl.Host, backupFile.Err)
					return
				}

			case <-time.After(settings.Timeout):
				util.Log.Errorf("timeout while waiting for backup from Mikrotik %s", settings.BaseUrl.Host)
				return
			}

			util.Log.Infof("backup file received from Mikrotik %s: %s (%d bytes)", settings.BaseUrl.Host, backupFile.File.Name, len(backupFile.File.Contents))

			go storage.UploadFile(settings, &backupFile.File, mainChannel)

			select {
			case _ = <-mainChannel:
			case <-time.After(settings.Timeout):
				util.Log.Errorf("timeout while waiting for upload the backup file %s", "TODO")
				return
			}
			util.Log.Infof("Mikrotik %s backup completed successfully", settings.BaseUrl.Host)
		}()
	}

	wg.Wait()
}

func createTargets(config *Config) []*backup.BackupSettings {
	targets := make([]*backup.BackupSettings, 0, len(config.Mikrotiks))

	for host, target := range config.Mikrotiks {
		util.Log.Infof("Processing Mikrotik %s, values: %s", host, target)
		u, err := util.CreateUrl(host, target.Username, target.Password)
		if err != nil {
			util.Log.Errorf("failed to create URL for Mikrotik %s: %v", host, err)
			continue
		}
		t, err := url.Parse(target.DownloadTo)
		if err != nil {
			util.Log.Errorf("failed to parse download URL for Mikrotik %s: %v", host, err)
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
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/tiktocker")
	viper.AddConfigPath(".")
	viper.SetEnvPrefix("TT")
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	pflag.String("log.level", "", "Log level (overrides yaml file)")
	pflag.Parse()
	_ = viper.BindPFlags(pflag.CommandLine)

	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %s", err))
	}

	var config *Config
	err = viper.Unmarshal(&config)
	if err != nil {
		log.Fatalf("Unable to decode into struct, %v", err)
		return config, err
	}

	return config, nil
}
