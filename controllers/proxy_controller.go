package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/3box/go-proxy/common/config"
	"github.com/3box/go-proxy/common/logging"
	"github.com/3box/go-proxy/common/metric"
)

type ProxyController interface {
	ProxyPostRequest(c *gin.Context)
	ProxyGetRequest(c *gin.Context)
	ProxyPutRequest(c *gin.Context)
	ProxyDeleteRequest(c *gin.Context)
	ProxyOptionsRequest(c *gin.Context)
}

type proxyController struct {
	ctx         context.Context
	cfg         *config.Config
	logger      logging.Logger
	metrics     metric.MetricService
	target      *url.URL
	mirror      *url.URL
	client      *http.Client
	activeConns *int64
}

type requestType string

const (
	proxyRequest  requestType = "proxy"
	mirrorRequest requestType = "mirror"
)

// Create a struct to hold request context
type requestContext struct {
	reqType    requestType
	ginContext *gin.Context
	request    *http.Request
	bodyBytes  []byte
	startTime  time.Time
	targetURL  *url.URL
	traceID    string
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

	pc := &proxyController{
		ctx:         ctx,
		cfg:         cfg,
		logger:      logger,
		metrics:     metrics,
		target:      target,
		mirror:      mirror,
		activeConns: new(int64),
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		DisableKeepAlives:   false,
		DisableCompression:  true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: cfg.Proxy.DialTimeout,
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}

	pc.client = &http.Client{
		Transport: transport,
		Timeout:   cfg.Proxy.Timeout,
	}

	return pc
}

func (_this *proxyController) proxyAndMirrorRequest(c *gin.Context) {
	// Generate or get trace ID
	traceID := c.GetHeader("X-Trace-ID")
	if traceID == "" {
		traceID = uuid.New().String()
	}

	// Read the original request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		_this.logger.Errorw("failed to read request body",
			"error", err,
			"trace_id", traceID,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read request"})
		return
	}
	_ = c.Request.Body.Close()

	// Restore the request body for downstream middleware/handlers
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	_this.processRequest(c, proxyRequest, bodyBytes, _this.target, traceID)
	if _this.mirror != nil {
		go _this.processRequest(c, mirrorRequest, bodyBytes, _this.mirror, traceID)
	}
}

func (_this *proxyController) processRequest(
	c *gin.Context,
	reqType requestType,
	bodyBytes []byte,
	targetURL *url.URL,
	traceID string,
) {
	// Instead of cloning, create a new request.
	targetPath := c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		targetPath += "?" + c.Request.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(
		c.Request.Context(),
		c.Request.Method,
		targetURL.String()+targetPath,
		bytes.NewBuffer(bodyBytes),
	)
	if err != nil {
		_this.logger.Errorw(
			fmt.Sprintf("failed to create %s request", reqType),
			"error", err,
			"trace_id", traceID,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create request"})
		return
	}

	// Copy headers from original request
	for k, vv := range c.Request.Header {
		req.Header[k] = vv
	}
	req.Header.Set("X-Trace-ID", traceID)

	if len(bodyBytes) > 0 {
		req.ContentLength = int64(len(bodyBytes))
	}

	_this.sendRequest(requestContext{
		reqType:    reqType,
		ginContext: c,
		request:    req,
		bodyBytes:  bodyBytes,
		startTime:  time.Now(),
		targetURL:  targetURL,
		traceID:    traceID,
	})
}

func (_this *proxyController) sendRequest(reqCtx requestContext) {
	// Set metric name based on request type
	metricName := metric.MetricProxyRequest
	if reqCtx.reqType == mirrorRequest {
		metricName = metric.MetricMirrorRequest
	}

	atomic.AddInt64(_this.activeConns, 1)
	// Make the request
	resp, err := _this.client.Do(reqCtx.request)
	atomic.AddInt64(_this.activeConns, -1)

	if err != nil {
		_this.logger.Errorw(fmt.Sprintf("%s error", reqCtx.reqType),
			"error", err,
			"trace_id", reqCtx.traceID,
		)
		if reqCtx.reqType == proxyRequest {
			reqCtx.ginContext.JSON(http.StatusBadGateway, gin.H{"error": "proxy error"})
		}
		return
	}
	// Ignore error since we are closing the body anyway
	defer func() { _ = resp.Body.Close() }()

	// Record status metrics with the appropriate prefix
	statusClass := fmt.Sprintf("%dxx", resp.StatusCode/100)
	_ = _this.metrics.RecordRequest(
		_this.ctx,
		fmt.Sprintf("%s_status", metricName),
		statusClass,
		reqCtx.request.URL.Path,
		attribute.String("status_class", statusClass),
		attribute.Int("status_code", resp.StatusCode),
		attribute.String("method", reqCtx.request.Method),
	)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		_this.logger.Errorw(fmt.Sprintf("failed to read %s response", reqCtx.reqType),
			"error", err,
			"trace_id", reqCtx.traceID,
		)
		if reqCtx.reqType == proxyRequest {
			reqCtx.ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read response"})
		}
		return
	}

	if reqCtx.reqType == proxyRequest {
		for k, vv := range resp.Header {
			for _, v := range vv {
				reqCtx.ginContext.Header(k, v)
			}
		}
		reqCtx.ginContext.Header("X-Proxied-By", config.ServiceName)
		reqCtx.ginContext.Header("X-Trace-ID", reqCtx.traceID)
		reqCtx.ginContext.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
	}

	// Record duration with the same metric name prefix
	_ = _this.metrics.RecordDuration(_this.ctx, metricName, time.Since(reqCtx.startTime),
		attribute.String("method", reqCtx.request.Method),
		attribute.String("path", reqCtx.request.URL.Path),
		attribute.Int("status_code", resp.StatusCode),
	)
}

func (_this *proxyController) ProxyGetRequest(c *gin.Context)     { _this.proxyAndMirrorRequest(c) }
func (_this *proxyController) ProxyPostRequest(c *gin.Context)    { _this.proxyAndMirrorRequest(c) }
func (_this *proxyController) ProxyPutRequest(c *gin.Context)     { _this.proxyAndMirrorRequest(c) }
func (_this *proxyController) ProxyDeleteRequest(c *gin.Context)  { _this.proxyAndMirrorRequest(c) }
func (_this *proxyController) ProxyOptionsRequest(c *gin.Context) { _this.proxyAndMirrorRequest(c) }
