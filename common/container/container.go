package container

import (
	"context"

	"go.uber.org/dig"

	"github.com/smrz2001/go-mirror/common/config"
	"github.com/smrz2001/go-mirror/common/logging"
	"github.com/smrz2001/go-mirror/common/metric"
	"github.com/smrz2001/go-mirror/controllers"
	"github.com/smrz2001/go-mirror/server"
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
