package main

import (
	"context"
	"log"
	"runtime"

	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/container"
	"github.com/3box/go-mirror/common/logging"
	"github.com/3box/go-mirror/server"
)

func main() {
	serverCtx := context.Background()
	ctr, err := container.BuildContainer(serverCtx)
	if err != nil {
		log.Fatalf("Failed to build container: %v", err)
	}

	if err = ctr.Invoke(func(
		c *config.Config,
		l logging.Logger,
		s server.Server,
	) error {
		l.Infow("starting db api",
			"architecture", runtime.GOARCH,
			"operating system", runtime.GOOS,
			"go version", runtime.Version(),
		)

		s.Run()
		return nil
	}); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
