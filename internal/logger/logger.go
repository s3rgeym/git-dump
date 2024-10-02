package logger

import (
	"github.com/sirupsen/logrus"
)

var logger = logrus.New()

func SetupLogger(logLevel string) {
	logger.SetFormatter(&logrus.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		logger.Fatalf("Invalid log level: %v", err)
	}
	logger.SetLevel(level)
}

func Debugf(format string, args ...interface{}) {
	logger.Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	logger.Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	logger.Warnf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	logger.Errorf(format, args...)
}

func Fatalf(format string, args ...interface{}) {
	logger.Fatalf(format, args...)
}

func Info(args ...interface{}) {
	logger.Info(args...)
}
