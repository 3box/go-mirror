package metric

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdk "go.opentelemetry.io/otel/sdk/metric"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-proxy/common/logging"
)

const serviceName = "go-mirror"

var _ MetricService = &otelMetricService{}

type otelMetricService struct {
	meterProvider *sdk.MeterProvider
	meter         metric.Meter
	logger        logging.Logger
	reader        *prometheus.Exporter
}

func NewOTelMetricService() (MetricService, error) {
	// Create a new Prometheus exporter
	exporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus exporter: %w", err)
	}

	// Create a new MeterProvider with the Prometheus exporter
	provider := sdk.NewMeterProvider(
		sdk.WithReader(exporter),
	)

	meter := provider.Meter(serviceName)

	return &otelMetricService{
		meter:  meter,
		reader: exporter,
	}, nil
}

func (_this otelMetricService) GetPrometheusHandler() gin.HandlerFunc {
	return gin.WrapH(promhttp.Handler())
}

// Add path normalization rules
func normalizePath(path string) string {
	// Split path into segments
	segments := strings.Split(strings.TrimPrefix(path, "/api/v0/"), "/")

	if len(segments) == 0 {
		return "/"
	}

	// Define patterns to normalize
	firstSegment := segments[0]
	switch firstSegment {
	case "node":
		return "/node"
	case "streams":
		return "/streams"
	default:
		// For paths that don't match any patterns
		return "/other"
	}
}

func (_this otelMetricService) RecordRequest(ctx context.Context, name, method, path string, attrs ...attribute.KeyValue) error {
	// Normalize the path before recording metrics
	normalizedPath := normalizePath(path)

	counter, err := _this.meter.Int64Counter(
		fmt.Sprintf("%s_requests_total", name),
		metric.WithDescription("Total number of requests received"),
	)
	if err != nil {
		return err
	}

	defaultAttrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("path", normalizedPath), // Use normalized path
	}
	counter.Add(ctx, 1, metric.WithAttributes(append(defaultAttrs, attrs...)...))
	return nil
}

func (_this otelMetricService) RecordDuration(ctx context.Context, name string, duration time.Duration, attrs ...attribute.KeyValue) error {
	// Find and normalize any path attributes
	normalizedAttrs := make([]attribute.KeyValue, len(attrs))
	for i, attr := range attrs {
		if attr.Key == "path" {
			normalizedAttrs[i] = attribute.String("path", normalizePath(attr.Value.AsString()))
		} else {
			normalizedAttrs[i] = attr
		}
	}

	histogram, err := _this.meter.Float64Histogram(
		fmt.Sprintf("%s_duration_seconds", name),
		metric.WithDescription("Duration of operation in seconds"),
	)
	if err != nil {
		return err
	}

	histogram.Record(ctx, duration.Seconds(), metric.WithAttributes(normalizedAttrs...))
	return nil
}
