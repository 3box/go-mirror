package metric

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdk "go.opentelemetry.io/otel/sdk/metric"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gin-gonic/gin"

	"github.com/3box/go-proxy/common/config"
	"github.com/3box/go-proxy/common/logging"
)

var _ MetricService = &otelMetricService{}

type otelMetricService struct {
	meterProvider *sdk.MeterProvider
	meter         metric.Meter
	logger        logging.Logger
	reader        *prometheus.Exporter
	gauges        *sync.Map
}

func NewOTelMetricService(logger logging.Logger) (MetricService, error) {
	// Create a new Prometheus exporter
	exporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus exporter: %w", err)
	}

	// Create a new MeterProvider with the Prometheus exporter
	provider := sdk.NewMeterProvider(
		sdk.WithReader(exporter),
	)

	meter := provider.Meter(config.ServiceName)

	return &otelMetricService{
		meter:  meter,
		reader: exporter,
		gauges: new(sync.Map),
		logger: logger,
	}, nil
}

func (_this *otelMetricService) GetPrometheusHandler() gin.HandlerFunc {
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

func (_this *otelMetricService) RecordRequest(ctx context.Context, name, method, path string, attrs ...attribute.KeyValue) error {
	// Normalize the path before recording metrics
	normalizedPath := normalizePath(path)

	counter, err := _this.meter.Int64Counter(
		fmt.Sprintf("%s_%s_requests_total", config.ServiceName, name),
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

func (_this *otelMetricService) RecordDuration(ctx context.Context, name, method, path string, duration time.Duration, attrs ...attribute.KeyValue) error {
	// Normalize the path before recording metrics
	normalizedPath := normalizePath(path)

	histogram, err := _this.meter.Float64Histogram(
		fmt.Sprintf("%s_%s_duration_seconds", config.ServiceName, name),
		metric.WithDescription("Duration of operation in seconds"),
	)
	if err != nil {
		return err
	}

	defaultAttrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("path", normalizedPath), // Use normalized path
	}
	histogram.Record(ctx, duration.Seconds(), metric.WithAttributes(append(defaultAttrs, attrs...)...))
	return nil
}

func (_this *otelMetricService) RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue) error {
	gaugeKey := fmt.Sprintf("%s_%s", config.ServiceName, name)

	gaugeInterface, _ := _this.gauges.LoadOrStore(gaugeKey, &struct {
		gauge metric.Float64ObservableGauge
		value atomic.Value
		once  sync.Once
	}{})

	gaugeData := gaugeInterface.(*struct {
		gauge metric.Float64ObservableGauge
		value atomic.Value
		once  sync.Once
	})

	gaugeData.once.Do(func() {
		gauge, err := _this.meter.Float64ObservableGauge(
			gaugeKey,
			metric.WithDescription("Gauge measurement"),
			metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
				val := gaugeData.value.Load()
				if val != nil {
					o.Observe(val.(float64), metric.WithAttributes(attrs...))
				}
				return nil
			}),
		)
		if err != nil {
			_this.logger.Errorw("failed to create gauge", "error", err)
			return
		}
		gaugeData.gauge = gauge
	})

	// Store the new value directly
	gaugeData.value.Store(value)
	return nil
}
