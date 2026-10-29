// Package telemetry configures the OpenTelemetry SDK for traces, metrics and
// logs.
//
// Exporters are chosen by the standard OTEL_* environment variables
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_TRACES_EXPORTER, ...), so the same binary
// can export to the collector, print to the console, or export nothing.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup installs global tracer, meter and logger providers, the W3C trace
// context propagator, and Go runtime metrics. The returned function flushes
// and stops everything; call it on shutdown.
func Setup(ctx context.Context, serviceName, serviceVersion string) (func(context.Context) error, error) {
	var shutdowns []func(context.Context) error
	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, fn := range slices.Backward(shutdowns) {
			errs = append(errs, fn(ctx))
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (func(context.Context) error, error) {
		return nil, errors.Join(err, shutdown(ctx))
	}

	res, err := resource.New(ctx,
		// Defaults first: OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override them.
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return fail(fmt.Errorf("span exporter: %w", err))
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(spanExporter),
		sdktrace.WithResource(res),
	)
	shutdowns = append(shutdowns, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fail(fmt.Errorf("metric reader: %w", err))
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(metricReader),
		sdkmetric.WithResource(res),
	)
	shutdowns = append(shutdowns, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	logExporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return fail(fmt.Errorf("log exporter: %w", err))
	}
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	shutdowns = append(shutdowns, loggerProvider.Shutdown)
	otel.SetLoggerProvider(loggerProvider)

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return fail(fmt.Errorf("runtime metrics: %w", err))
	}
	return shutdown, nil
}

// Logger returns a logger that writes JSON to stdout (for `docker logs`) and
// sends every record to the OpenTelemetry logs pipeline. Records logged with
// a context, such as logger.InfoContext(ctx, ...), carry that context's trace
// and span IDs, which is what links logs to traces.
func Logger(scope string) *slog.Logger {
	return slog.New(slog.NewMultiHandler(
		slog.NewJSONHandler(os.Stdout, nil),
		otelslog.NewHandler(scope),
	))
}
