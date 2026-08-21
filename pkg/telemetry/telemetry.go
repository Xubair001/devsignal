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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
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
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
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

	return tp.Shutdown, nil
}
