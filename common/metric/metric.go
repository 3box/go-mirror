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
	MetricProxyRequest            = "proxy_request"
	MetricMirrorRequest           = "mirror_request"
	MetricPanicRecovered          = "panic_recovered"
	MetricProxyActiveConnections  = "proxy_active_connections"
	MetricMirrorActiveConnections = "mirror_active_connections"
)
