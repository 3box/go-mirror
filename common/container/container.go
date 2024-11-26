package container

import (
	"context"

	"go.uber.org/dig"

	"github.com/3box/go-mirror/common/cert"
	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/logging"
	"github.com/3box/go-mirror/common/metric"
	"github.com/3box/go-mirror/controllers"
	"github.com/3box/go-mirror/server"
)

func BuildContainer(ctx context.Context) (*dig.Container, error) {
	container := dig.New()
	var err error

	// Provide logging
	if err = container.Provide(logging.NewLogger); err != nil {
		return nil, err
	}

	// Provide context
	if err = container.Provide(func() context.Context {
		return ctx
	}); err != nil {
		return nil, err
	}

	// Provide config
	if err = container.Provide(config.LoadConfig); err != nil {
		return nil, err
	}

	// Provide metrics
	if err = container.Provide(metric.NewOTelMetricService); err != nil {
		return nil, err
	}

	// Provide cert manager
	if err = container.Provide(cert.NewACMECertManager); err != nil {
		return nil, err
	}

	// Provide handlers
	if err = container.Provide(controllers.NewProxyController); err != nil {
		return nil, err
	}

	// Provide server
	if err = container.Provide(server.NewServer); err != nil {
		return nil, err
	}

	return container, nil
}
