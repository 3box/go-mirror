package metric

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"go.opentelemetry.io/otel/attribute"
)

type MetricService interface {
	GetPrometheusHandler() gin.HandlerFunc
	RecordRequest(ctx context.Context, name, method, path string, attrs ...attribute.KeyValue) error
	RecordDuration(ctx context.Context, name string, duration time.Duration, attrs ...attribute.KeyValue) error
	RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue) error
}

const (
	// Core proxy/mirror operation metrics
	MetricProxy  = "proxy"  // Base metric for proxy operations
	MetricMirror = "mirror" // Base metric for mirror operations

	// Connection tracking metrics
	MetricProxyConnections  = "proxy_connections"  // For active proxy connections
	MetricMirrorConnections = "mirror_connections" // For active mirror connections

	// System metrics
	MetricPanics = "panics" // For system panic tracking
)
