package telemetry

import (
	"context"
	"fmt"

	"github.com/qiankunli/case-code-review/internal/console"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	stdoutmetric "go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
)

// newStdoutTraceExporter creates a console trace exporter with pretty-print output.
func newStdoutTraceExporter() (*stdouttrace.Exporter, error) {
	return stdouttrace.New(stdouttrace.WithPrettyPrint())
}

// newStdoutMetricExporter creates a console metric exporter with pretty-print output.
func newStdoutMetricExporter() (sdkmetric.Exporter, error) {
	return stdoutmetric.New(stdoutmetric.WithPrettyPrint())
}

// initOTLPProviders sets up OTLP gRPC exporters for traces and metrics.
func initOTLPProviders(ctx context.Context, res *resource.Resource, cfg Config) {
	traceExp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
	)
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr] WARNING: failed to create OTLP trace exporter: %v\n", err)
		return
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	tracerProvider = tp
	shutdownFuncs = append(shutdownFuncs, func(ctx context.Context) error { return tp.Shutdown(ctx) })

	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint),
	)
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr] WARNING: failed to create OTLP metric exporter: %v\n", err)
		return
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	meterProvider = mp
	shutdownFuncs = append(shutdownFuncs, func(ctx context.Context) error { return mp.Shutdown(ctx) })
}

// initConsoleProviders sets up stdout exporters for traces and metrics.
func initConsoleProviders(res *resource.Resource) {
	traceExp, err := newStdoutTraceExporter()
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr] WARNING: failed to create console trace exporter: %v\n", err)
		return
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	tracerProvider = tp
	shutdownFuncs = append(shutdownFuncs, func(ctx context.Context) error { return tp.Shutdown(ctx) })

	metricExp, err := newStdoutMetricExporter()
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr] WARNING: failed to create console metric exporter: %v\n", err)
		return
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	meterProvider = mp
	shutdownFuncs = append(shutdownFuncs, func(ctx context.Context) error { return mp.Shutdown(ctx) })
}
