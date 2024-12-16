package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"go.opentelemetry.io/otel/attribute"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-proxy/common/config"
	"github.com/3box/go-proxy/common/logging"
	"github.com/3box/go-proxy/common/metric"
	"github.com/3box/go-proxy/controllers"
)

type Server interface {
	Run()
}

type serverImpl struct {
	ctx             context.Context
	serverCtx       context.Context
	serverCtxCancel context.CancelFunc
	cfg             *config.Config
	logger          logging.Logger
	proxyServer     *http.Server
	metricsServer   *http.Server
	proxyController controllers.ProxyController
	metricService   metric.MetricService
	wg              *sync.WaitGroup
}

func NewServer(
	ctx context.Context,
	cfg *config.Config,
	logger logging.Logger,
	metricService metric.MetricService,
	proxyController controllers.ProxyController,
) (*gin.Engine, Server) {
	router := gin.New()

	// Set up a server context
	serverCtx, serverCtxCancel := context.WithCancel(ctx)

	server := &serverImpl{
		ctx:             ctx,
		serverCtx:       serverCtx,
		serverCtxCancel: serverCtxCancel,
		cfg:             cfg,
		logger:          logger,
		proxyServer: &http.Server{
			Handler: router,
			Addr:    ":" + cfg.Proxy.ListenPort,
			BaseContext: func(net.Listener) context.Context {
				return serverCtx
			},
		},
		metricsServer: &http.Server{
			Handler: router,
			Addr:    ":" + cfg.Metrics.ListenPort,
		},
		proxyController: proxyController,
		metricService:   metricService,
		wg:              &sync.WaitGroup{},
	}

	// Add the panic recovery middleware before any routes
	router.Use(server.panicHandler())

	// Match all paths including root
	router.Any("/*path", server.router)

	return router, server
}

func (_this serverImpl) router(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		{
			if strings.HasPrefix(c.Request.URL.Path, "/metrics") {
				_this.metricService.GetPrometheusHandler()(c)
				return
			}

			_this.proxyController.ProxyGetRequest(c)
		}
	case http.MethodPost:
		_this.proxyController.ProxyPostRequest(c)
	case http.MethodPut:
		_this.proxyController.ProxyPutRequest(c)
	case http.MethodDelete:
		_this.proxyController.ProxyDeleteRequest(c)
	case http.MethodOptions:
		_this.proxyController.ProxyOptionsRequest(c)
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (_this serverImpl) Run() {
	// Start the proxy server
	_this.runProxyServer()

	// Start the metrics server
	_this.runMetricsServer()

	// Graceful shutdown
	_this.gracefulShutdown()

	// Wait for goroutines to finish
	_this.wg.Wait()
}

func (_this serverImpl) runProxyServer() {
	_this.wg.Add(1)
	go func() {
		defer _this.wg.Done()

		_this.logger.Infof("server: proxy server starting on %s", _this.proxyServer.Addr)
		err := _this.proxyServer.ListenAndServe()

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_this.logger.Fatalf("proxy server listen error: %s", err)
		}
	}()
}

func (_this serverImpl) runMetricsServer() {
	_this.wg.Add(1)
	go func() {
		defer _this.wg.Done()

		_this.logger.Infof("server: metrics server starting on %s", _this.metricsServer.Addr)
		err := _this.metricsServer.ListenAndServe()

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_this.logger.Fatalf("metrics server listen error: %s", err)
		}
	}()
}

func (_this serverImpl) gracefulShutdown() {
	_this.wg.Add(1)
	go func() {
		defer _this.wg.Done()

		// Wait for interrupt signal to gracefully shutdown the server
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP)
		<-quit

		_this.serverCtxCancel()
		_this.logger.Infof("server: shutdown started...")

		if errs := _this.shutdownServers(); errs != nil {
			_this.logger.Fatalf("server: shutdown error(s): %v", errs)
		}

		_this.logger.Infof("server: shutdown complete")
		_ = _this.logger.Sync()
	}()
}

func (_this serverImpl) shutdownServers() (errs []error) {
	// Let shutdown take as long the parent context allows
	if err := _this.proxyServer.Shutdown(_this.ctx); err != nil {
		errs = append(errs, fmt.Errorf("proxy server shutdown error: %w", err))
	}
	if err := _this.metricsServer.Shutdown(_this.ctx); err != nil {
		errs = append(errs, fmt.Errorf("metrics server shutdown error: %w", err))
	}
	return errs
}

func (_this serverImpl) panicHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Record panic metric
				attrs := []attribute.KeyValue{
					attribute.String("method", c.Request.Method),
					attribute.String("path", c.Request.URL.Path),
					attribute.String("error", fmt.Sprintf("%v", err)),
				}

				if recordErr := _this.metricService.RecordRequest(_this.ctx, metric.MetricPanicRecovered, c.Request.Method, c.Request.URL.Path, attrs...); recordErr != nil {
					_this.logger.Errorw("failed to record panic metric", "error", recordErr)
				}

				// Log the panic with stack trace
				stack := make([]byte, 4096)
				stack = stack[:runtime.Stack(stack, false)]
				_this.logger.Errorw("panic recovered",
					"error", err,
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"stack", string(stack),
				)

				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "internal server error",
				})
			}
		}()

		// Process request
		c.Next()
	}
}
