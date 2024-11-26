package config

import (
	"strings"

	"github.com/spf13/viper"

	"github.com/3box/go-mirror/common/logging"
)

type Config struct {
	Cert  CertConfig
	Proxy ProxyConfig
}

type CertConfig struct {
	Enabled    bool
	Domains    []string
	CacheDir   string
	TestMode   bool
	ListenAddr string
}

type ProxyConfig struct {
	TargetURL  string
	MirrorURL  string
	ListenAddr string
	TLSEnabled bool
}

func LoadConfig(logger logging.Logger) (*Config, error) {
	// Create a new viper instance with the experimental bind struct feature enabled
	v := viper.NewWithOptions(
		viper.ExperimentalBindStruct(),
		// This was necessary to get viper to recognize the nested struct fields
		viper.EnvKeyReplacer(strings.NewReplacer(".", "_")),
	)
	v.SetEnvPrefix("GO_MIRROR")
	v.AutomaticEnv()

	// Unmarshal environment variables into the config struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	logger.Infow("config loaded successfully",
		"config", cfg,
	)

	return &cfg, nil
}
