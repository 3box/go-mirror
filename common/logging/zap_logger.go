package logging

import (
	"embed"
	"log"
	"os"

	"gopkg.in/yaml.v3"

	"go.uber.org/zap"
)

//go:embed zap_logger.yml
var zapYamlFile embed.FS

func NewLogger() Logger {
	configYaml, err := zapYamlFile.ReadFile("zap_logger.yml")
	if err != nil {
		log.Fatalf("logger: failed to read logger configuration: %s", err)
	}
	var zapConfig *zap.Config
	if err = yaml.Unmarshal(configYaml, &zapConfig); err != nil {
		log.Fatalf("logger: failed to unmarshal zap logger configuration: %s", err)
	}

	level := zap.NewAtomicLevelAt(zap.InfoLevel)
	logLevel := os.Getenv("LOG_LEVEL")
	if len(logLevel) > 0 {
		if parsedLevel, err := zap.ParseAtomicLevel(logLevel); err != nil {
			log.Fatalf("logger: error parsing log level %s: %v", logLevel, err)
		} else {
			level = parsedLevel
		}
	}
	zapConfig.Level = level
	zapConfig.Encoding = "json"
	baseLogger := zap.Must(zapConfig.Build())
	sugaredLogger := baseLogger.Sugar()
	return sugaredLogger
}
