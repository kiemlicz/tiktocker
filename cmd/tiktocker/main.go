package main

import (
	"fmt"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"strings"
	"tiktocker/internal/util"
)

func main() {
	setupConfig()
	util.Setup()
	util.Log.Infof("Mikrotik Backup starts")
}

func setupConfig() {
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
}
