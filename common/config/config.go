package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/3box/go-proxy/common/logging"
)

const (
	defaultProxyListenPort   = "8080"
	defaultMetricsListenPort = "9464"
	defaultDialTimeout       = 30 * time.Second
	defaultIdleTimeout       = 90 * time.Second
	defaultMirrorTimeout     = 30 * time.Second
)

type Config struct {
	Proxy   ProxyConfig
	Metrics MetricsConfig
}

type ProxyConfig struct {
	TargetURL     string
	MirrorURL     string
	ListenPort    string
	DialTimeout   time.Duration
	IdleTimeout   time.Duration
	MirrorTimeout time.Duration
}

type MetricsConfig struct {
	Enabled    bool
	ListenPort string
}

func LoadConfig(logger logging.Logger) (*Config, error) {
	// Create a new viper instance with the experimental bind struct feature enabled
	v := viper.NewWithOptions(
		viper.ExperimentalBindStruct(),
		// This was necessary to get viper to recognize the nested struct fields
		viper.EnvKeyReplacer(strings.NewReplacer(".", "_")),
	)
	v.SetEnvPrefix("GO_PROXY")
	v.AutomaticEnv()

	v.SetDefault("Proxy.ListenPort", defaultProxyListenPort)
	v.SetDefault("Proxy.DialTimeout", defaultDialTimeout)
	v.SetDefault("Proxy.IdleTimeout", defaultIdleTimeout)
	v.SetDefault("Proxy.MirrorTimeout", defaultMirrorTimeout)
	v.SetDefault("Metrics.ListenPort", defaultMetricsListenPort)

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
