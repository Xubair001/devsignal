// Package telemetry wires OpenTelemetry tracing. Exports to stdout until a
// collector exists; the exporter is a config value so that swap is not a code
// change (blueprint §28).
//
// Sampling note: ingestion must emit one span per BATCH, not per document.
// Per-document spans on this pipeline cost more than the compute they observe.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

type Config struct {
	Enabled     bool
	Exporter    string
	ServiceName string
	Env         string
	SampleRatio float64
}

// Shutdown flushes pending spans. Call it on the way out or you lose the traces
// for the shutdown path itself, which is exactly when you want them.
type Shutdown func(context.Context) error

func Init(ctx context.Context, cfg Config) (Shutdown, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	var exp sdktrace.SpanExporter
	var err error
	switch cfg.Exporter {
	case "stdout", "":
		// stderr for the same reason as metrics below: stdout is the command's.
		exp, err = stdouttrace.New(
			stdouttrace.WithPrettyPrint(), stdouttrace.WithWriter(os.Stderr))
	default:
		return nil, fmt.Errorf("telemetry: unsupported exporter %q (stdout only until a collector exists)", cfg.Exporter)
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: exporter: %w", err)
	}

	// NewSchemaless, not NewWithAttributes: resource.Default() already carries a
	// schema URL, and merging two different ones is a hard error rather than a
	// warning. Schemaless attributes merge cleanly against any Default.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(cfg.ServiceName),
		attribute.String("deployment.environment.name", cfg.Env),
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		// ParentBased so a sampled inbound request keeps its whole trace.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	// Metrics, on a periodic reader. A separate provider from tracing because the
	// two are sampled differently: traces are sampled hard, metrics never are.
	// Sampling a counter does not reduce its cost, it makes it wrong.
	// To stderr, not stdout. A CLI role's stdout is its report, and a metrics dump
	// landing in the middle of it makes the output unparseable by anything —
	// including the operator reading it.
	mexp, err := stdoutmetric.New(stdoutmetric.WithWriter(os.Stderr))
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		// A minute, not the 10-second default: the SLO windows are hours and days,
		// and a shorter interval buys nothing but volume.
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp,
			sdkmetric.WithInterval(time.Minute))),
	)
	otel.SetMeterProvider(mp)
	if err := InitMetrics(mp.Meter("devsignal")); err != nil {
		return nil, err
	}

	// Both providers flush, and a failure in either is reported. Losing metrics on
	// the way out means losing the window that contains the shutdown.
	return func(ctx context.Context) error {
		terr := tp.Shutdown(ctx)
		merr := mp.Shutdown(ctx)
		if terr != nil {
			return terr
		}
		return merr
	}, nil
}
