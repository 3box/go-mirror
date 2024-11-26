package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/logging"
	"github.com/3box/go-mirror/common/metric"
	"github.com/3box/go-mirror/controllers"
)

type Server interface {
	Run()
}

// serverImpl is the struct that implements the server interface. It pulls together the individual implementations for
// the different controllers.
type serverImpl struct {
	ctx             context.Context
	cfg             *config.Config
	logger          logging.Logger
	server          *http.Server
	proxyController controllers.ProxyController
	metrics         metric.MetricService
}

func NewServer(
	ctx context.Context,
	cfg *config.Config,
	logger logging.Logger,
	metrics metric.MetricService,
	proxyController controllers.ProxyController,
) (*gin.Engine, Server) {
	router := gin.New()

	server := &serverImpl{
		ctx:    ctx,
		cfg:    cfg,
		logger: logger,
		server: &http.Server{
			Handler: router,
			Addr:    cfg.Proxy.ListenAddr,
		},
		proxyController: proxyController,
		metrics:         metrics,
	}

	// Match all paths including root
	router.Any("/*path", server.handleProxy)

	return router, server
}

func (_this serverImpl) handleProxy(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/metrics") {
		_this.metrics.GetPrometheusHandler()(c)
		return
	}

	switch c.Request.Method {
	case http.MethodGet:
		_this.proxyController.ProxyGetRequest(c)
	case http.MethodPost:
		_this.proxyController.ProxyPostRequest(c)
	case http.MethodPut:
		_this.proxyController.ProxyPutRequest(c)
	case http.MethodDelete:
		_this.proxyController.ProxyDeleteRequest(c)
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (_this serverImpl) Run() {
	// Set up a server context
	serverCtx, serverCtxCancel := context.WithCancel(_this.ctx)
	_this.server.BaseContext = func(net.Listener) context.Context {
		return serverCtx
	}

	wg := sync.WaitGroup{}
	wg.Add(2)

	// Start the proxy server
	go func() {
		defer wg.Done()

		_this.logger.Infof("server: proxy server starting on %s", _this.server.Addr)
		err := _this.server.ListenAndServe()

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_this.logger.Fatalf("proxy server listen error: %s", err)
		}
	}()

	// Graceful shutdown
	go func() {
		defer wg.Done()

		// Wait for interrupt signal to gracefully shutdown the server
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)
		<-quit

		serverCtxCancel()
		_this.logger.Infof("server: shutdown started...")

		// Let shutdown take as long the parent context allows
		if err := _this.Stop(); err != nil {
			_this.logger.Fatalf("server: shutdown error: %s", err)
		}

		_this.logger.Infof("server: shutdown complete")
		_ = _this.logger.Sync()
	}()

	// Wait for both goroutines to finish
	wg.Wait()
}

func (_this serverImpl) Stop() error {
	ctx, cancel := context.WithTimeout(_this.ctx, 5*time.Second)
	defer cancel()

	if err := _this.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("proxy server shutdown error: %w", err)
	}

	return nil
}
