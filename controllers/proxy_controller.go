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

	"github.com/3box/go-mirror/common/config"
	"github.com/3box/go-mirror/common/logging"
	"github.com/3box/go-mirror/common/metric"
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
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
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

	// Close the original body
	c.Request.Body.Close()

	start := time.Now()

	// Create the proxy request
	proxyReq := c.Request.Clone(c.Request.Context())
	proxyReq.URL.Scheme = _this.target.Scheme
	proxyReq.URL.Host = _this.target.Host
	proxyReq.Host = _this.target.Host
	proxyReq.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Log outbound request
	_this.logger.Debugw("outbound request",
		"method", proxyReq.Method,
		"url", proxyReq.URL.String(),
		"headers", proxyReq.Header,
	)

	// Record request with normalized path
	err = _this.metrics.RecordRequest(_this.ctx, metric.MetricProxyRequest, proxyReq.Method, proxyReq.URL.Path)
	if err != nil {
		_this.logger.Errorw("failed to record proxy request metric", "error", err)
	}

	// Make the proxy request
	resp, err := _this.transport.RoundTrip(proxyReq)
	if err != nil {
		_this.logger.Errorw("proxy error", "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "proxy error"})
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_this.logger.Errorw("failed to read proxy response body", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read response"})
		return
	}

	// Log response details
	_this.logger.Debugw("received proxy response",
		"status", resp.StatusCode,
		"content_length", len(respBody),
		"content_type", resp.Header.Get("Content-Type"),
	)

	attrs := []attribute.KeyValue{
		attribute.String("method", proxyReq.Method),
		attribute.String("path", proxyReq.URL.Path),
		attribute.Int("status_code", resp.StatusCode),
		// Group status codes into categories
		attribute.String("status_class", fmt.Sprintf("%dxx", resp.StatusCode/100)),
	}

	err = _this.metrics.RecordDuration(_this.ctx, metric.MetricProxyRequest, time.Since(start), attrs...)
	if err != nil {
		_this.logger.Errorw("failed to record proxy request duration metric", "error", err)
	}

	// Send mirror request if configured
	if _this.mirror != nil {
		// Create a new context for the mirror request
		mirrorReq := c.Request.Clone(_this.ctx)
		go _this.mirrorRequest(mirrorReq, bodyBytes)
	}

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Header(k, v)
		}
	}

	// Write the response
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
}

func (_this proxyController) mirrorRequest(mirrorReq *http.Request, bodyBytes []byte) {
	// Create a context with timeout for the mirror request
	ctx, cancel := context.WithTimeout(_this.ctx, 30*time.Second)
	defer cancel()

	// Use the new context
	mirrorReq = mirrorReq.WithContext(ctx)

	start := time.Now()

	// Create the mirror request
	mirrorReq.URL.Scheme = _this.mirror.Scheme
	mirrorReq.URL.Host = _this.mirror.Host
	mirrorReq.Host = _this.mirror.Host
	mirrorReq.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Log mirror request
	_this.logger.Debugw("mirror request",
		"method", mirrorReq.Method,
		"url", mirrorReq.URL.String(),
		"headers", mirrorReq.Header,
	)

	// Record request with normalized path
	err := _this.metrics.RecordRequest(_this.ctx, metric.MetricMirrorRequest, mirrorReq.Method, mirrorReq.URL.Path)
	if err != nil {
		_this.logger.Errorw("failed to record mirror request metric", "error", err)
	}

	// Make the mirror request
	resp, err := _this.transport.RoundTrip(mirrorReq)
	if err != nil {
		_this.logger.Errorw("mirror error", "error", err)
		return
	}
	defer resp.Body.Close()

	// Read and log the mirror response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		_this.logger.Errorw("failed to read mirror response", "error", err)
		return
	}

	// Log mirror response
	_this.logger.Debugw("mirror response",
		"status", resp.StatusCode,
		"content_length", len(body),
		"content_type", resp.Header.Get("Content-Type"),
	)

	attrs := []attribute.KeyValue{
		attribute.String("method", mirrorReq.Method),
		attribute.String("path", mirrorReq.URL.Path),
		attribute.Int("status_code", resp.StatusCode),
		// Group status codes into categories
		attribute.String("status_class", fmt.Sprintf("%dxx", resp.StatusCode/100)),
	}

	err = _this.metrics.RecordDuration(_this.ctx, metric.MetricMirrorRequest, time.Since(start), attrs...)
	if err != nil {
		_this.logger.Errorw("failed to record proxy request duration metric", "error", err)
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
