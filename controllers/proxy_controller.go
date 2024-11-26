package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-proxy/common/config"
	"github.com/3box/go-proxy/common/logging"
	"github.com/3box/go-proxy/common/metric"
)

type ProxyController interface {
	ProxyPostRequest(c *gin.Context)
	ProxyGetRequest(c *gin.Context)
	ProxyPutRequest(c *gin.Context)
	ProxyDeleteRequest(c *gin.Context)
}

type proxyController struct {
	ctx       context.Context
	cfg       *config.Config
	logger    logging.Logger
	metrics   metric.MetricService
	target    *url.URL
	mirror    *url.URL
	transport *http.Transport
}

type requestType string

const (
	proxyRequest  requestType = "proxy"
	mirrorRequest             = "mirror"
)

// Create a struct to hold request context
type requestContext struct {
	reqType    requestType
	ginContext *gin.Context
	request    *http.Request
	bodyBytes  []byte
	startTime  time.Time
	targetURL  *url.URL
}

func NewProxyController(
	ctx context.Context,
	cfg *config.Config,
	logger logging.Logger,
	metrics metric.MetricService,
) ProxyController {
	target, err := url.Parse(cfg.Proxy.TargetURL)
	if err != nil {
		logger.Fatalf("invalid target URL: %v", err)
	}
	var mirror *url.URL
	if cfg.Proxy.MirrorURL != "" {
		mirror, err = url.Parse(cfg.Proxy.MirrorURL)
		if err != nil {
			logger.Fatalf("invalid mirror URL: %v", err)
		}
	}
	// Configure transport with timeouts
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: cfg.Proxy.DialTimeout,
		}).DialContext,
		ForceAttemptHTTP2: true,
		IdleConnTimeout:   cfg.Proxy.IdleTimeout,
	}

	return &proxyController{
		ctx:       ctx,
		cfg:       cfg,
		logger:    logger,
		metrics:   metrics,
		target:    target,
		mirror:    mirror,
		transport: transport,
	}
}

func (_this proxyController) proxyRequest(c *gin.Context) {
	// Read the original request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		_this.logger.Errorw("failed to read request body", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request"})
		return
	}
	// Ignore error since we are closing the body anyway
	_ = c.Request.Body.Close()

	// Handle proxy request
	_this.handleRequest(requestContext{
		reqType:    proxyRequest,
		ginContext: c,
		request:    c.Request,
		bodyBytes:  bodyBytes,
		startTime:  time.Now(),
		targetURL:  _this.target,
	})

	// Handle mirror request if configured
	if _this.mirror != nil {
		go func() {
			ctx, cancel := context.WithTimeout(_this.ctx, _this.cfg.Proxy.MirrorTimeout)
			defer cancel()

			_this.handleRequest(requestContext{
				reqType:   mirrorRequest,
				request:   c.Request.Clone(ctx),
				bodyBytes: bodyBytes,
				startTime: time.Now(),
				targetURL: _this.mirror,
			})
		}()
	}
}

func (_this proxyController) handleRequest(reqCtx requestContext) {
	// Prepare the request
	req := reqCtx.request.Clone(reqCtx.request.Context())
	req.URL.Scheme = reqCtx.targetURL.Scheme
	req.URL.Host = reqCtx.targetURL.Host
	req.Host = reqCtx.targetURL.Host
	req.Body = io.NopCloser(bytes.NewBuffer(reqCtx.bodyBytes))

	// Log outbound request
	_this.logger.Debugw(fmt.Sprintf("%s request", reqCtx.reqType),
		"method", req.Method,
		"url", req.URL.String(),
		"headers", req.Header,
	)

	// Record metrics
	metricName := metric.MetricProxyRequest
	if reqCtx.reqType == mirrorRequest {
		metricName = metric.MetricMirrorRequest
	}

	if err := _this.metrics.RecordRequest(_this.ctx, metricName, req.Method, req.URL.Path); err != nil {
		_this.logger.Errorw("failed to record request metric", "error", err)
	}

	// Make the request
	resp, err := _this.transport.RoundTrip(req)
	if err != nil {
		_this.logger.Errorw(fmt.Sprintf("%s error", reqCtx.reqType), "error", err)
		if reqCtx.reqType == proxyRequest {
			reqCtx.ginContext.JSON(http.StatusBadGateway, gin.H{"error": "proxy error"})
		}
		return
	}
	// Ignore error since we are closing the body anyway
	defer func() { _ = resp.Body.Close() }()

	// Process response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_this.logger.Errorw(fmt.Sprintf("failed to read %s response", reqCtx.reqType), "error", err)
		if reqCtx.reqType == proxyRequest {
			reqCtx.ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read response"})
		}
		return
	}

	// Log response
	_this.logger.Debugw(fmt.Sprintf("%s response", reqCtx.reqType),
		"status", resp.StatusCode,
		"content length", len(respBody),
		"headers", resp.Header,
		"latency", time.Since(reqCtx.startTime),
	)

	// Record metrics
	attrs := []attribute.KeyValue{
		attribute.String("method", req.Method),
		attribute.String("path", req.URL.Path),
		attribute.Int("status_code", resp.StatusCode),
		attribute.String("status_class", fmt.Sprintf("%dxx", resp.StatusCode/100)),
	}

	if err := _this.metrics.RecordDuration(_this.ctx, metricName, time.Since(reqCtx.startTime), attrs...); err != nil {
		_this.logger.Errorw("failed to record duration metric", "error", err)
	}

	// Write response for proxy requests only
	if reqCtx.reqType == proxyRequest {
		for k, vv := range resp.Header {
			for _, v := range vv {
				reqCtx.ginContext.Header(k, v)
			}
		}
		reqCtx.ginContext.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
	}
}

// ProxyGetRequest handles GET requests
func (_this proxyController) ProxyGetRequest(c *gin.Context) {
	_this.proxyRequest(c)
}

// ProxyPostRequest handles POST requests
func (_this proxyController) ProxyPostRequest(c *gin.Context) {
	_this.proxyRequest(c)
}

// ProxyPutRequest handles PUT requests
func (_this proxyController) ProxyPutRequest(c *gin.Context) {
	_this.proxyRequest(c)
}

// ProxyDeleteRequest handles DELETE requests
func (_this proxyController) ProxyDeleteRequest(c *gin.Context) {
	_this.proxyRequest(c)
}
