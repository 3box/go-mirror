package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	ctx     context.Context
	cfg     *config.Config
	logger  logging.Logger
	metrics metric.MetricService
	target  *url.URL
	mirror  *url.URL
	client  *http.Client
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

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	return &proxyController{
		ctx:     ctx,
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
		target:  target,
		mirror:  mirror,
		client:  client,
	}
}

func (_this proxyController) createRequest(
	ctx context.Context,
	originalReq *http.Request,
	bodyBytes []byte,
) (*http.Request, error) {
	// Create new request with appropriate context
	newReq, err := http.NewRequestWithContext(
		ctx,
		originalReq.Method,
		originalReq.URL.String(),
		bytes.NewBuffer(bodyBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers from original request
	for k, vv := range originalReq.Header {
		if k != "Content-Length" { // Skip Content-Length as it will be set automatically
			newReq.Header[k] = vv
		}
	}

	// Set Content-Length once
	if len(bodyBytes) > 0 {
		newReq.ContentLength = int64(len(bodyBytes))
	}

	return newReq, nil
}

func (_this proxyController) proxyRequest(c *gin.Context) {
	// Read the original request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		_this.logger.Errorw("failed to read request body", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request"})
		return
	}
	_ = c.Request.Body.Close()

	// Restore the request body for downstream middleware/handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Create proxy request
	proxyReq, err := _this.createRequest(c.Request.Context(), c.Request, bodyBytes)
	if err != nil {
		_this.logger.Errorw("failed to create proxy request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create request"})
		return
	}

	// Handle proxy request
	_this.handleRequest(requestContext{
		reqType:    proxyRequest,
		ginContext: c,
		request:    proxyReq,
		bodyBytes:  bodyBytes,
		startTime:  time.Now(),
		targetURL:  _this.target,
	})

	// Handle mirror request if configured
	if _this.mirror != nil {
		go func() {
			ctx, cancel := context.WithTimeout(_this.ctx, _this.cfg.Proxy.MirrorTimeout)
			defer cancel()

			mirrorReq, err := _this.createRequest(ctx, c.Request, bodyBytes)
			if err != nil {
				_this.logger.Errorw("failed to create mirror request", "error", err)
				return
			}

			_this.handleRequest(requestContext{
				reqType:   mirrorRequest,
				request:   mirrorReq,
				bodyBytes: bodyBytes,
				startTime: time.Now(),
				targetURL: _this.mirror,
			})
		}()
	}
}

func (_this proxyController) handleRequest(reqCtx requestContext) {
	// Prepare the request
	req := reqCtx.request
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
	resp, err := _this.client.Do(req)
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
		"method", req.Method,
		"url", req.URL.String(),
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
		// Add a header indicating that this request was proxied
		reqCtx.ginContext.Header("X-Proxied-By", config.ServiceName)
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
