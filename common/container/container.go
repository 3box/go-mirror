package container

import (
	"context"

	"go.uber.org/dig"

	"github.com/3box/go-proxy/common/config"
	"github.com/3box/go-proxy/common/logging"
	"github.com/3box/go-proxy/common/metric"
	"github.com/3box/go-proxy/controllers"
	"github.com/3box/go-proxy/server"
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
