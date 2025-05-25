package util

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"strings"
)

var Log *logrus.Logger

func Setup() {
	Log = logrus.New()
	logLevel := viper.GetString("Log.level")
	level, err := logrus.ParseLevel(strings.ToLower(logLevel))
	if err != nil {
		Log.Warnf("Invalid Log level in config: %s. Using 'info'.", logLevel)
		level = logrus.InfoLevel
	}

	Log.SetLevel(level)
	Log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
}
