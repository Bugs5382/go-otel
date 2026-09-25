package otel

/*
MIT License

Copyright (c) 2026 Shane

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
*/

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Init configures global Tracer and Meter providers that export over OTLP gRPC
// (insecure) to otlpEndpoint, tagged with service.name=service. Traces and
// metrics ride the same endpoint and share one resource, so a single call wires
// both. The returned shutdown func flushes and closes both providers; callers
// should defer it.
//
// An empty (or all-whitespace) otlpEndpoint runs Init without a collector: the
// providers and the W3C propagator are installed as usual, so spans get real
// trace IDs and trace context still flows across process boundaries, but no
// exporter is built and spans and metrics are dropped. This suits local
// development, tests, and environments with no collector. Pass
// os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") to make export opt-in by
// environment.
func Init(ctx context.Context, service, otlpEndpoint string) (shutdown func(context.Context) error, err error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(service),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	var tp *sdktrace.TracerProvider
	var mp *sdkmetric.MeterProvider
	if strings.TrimSpace(otlpEndpoint) == "" {
		tp, mp = newLocalProviders(res)
	} else {
		tp, mp, err = newExportingProviders(ctx, res, otlpEndpoint)
		if err != nil {
			return nil, err
		}
	}
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	// Install a global text-map propagator so W3C trace context and baggage
	// flow across process boundaries. Without this, injected/extracted carriers
	// are no-ops and traces break at every hop.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		// Flush both pipelines. The metric reader performs a final export on
		// shutdown, so an unreachable collector surfaces here as an export
		// error; that is an environmental condition, not a caller wiring fault
		// (the trace batcher drops the same way), so hand it to the global
		// error handler rather than failing shutdown. Genuine trace lifecycle
		// errors still propagate.
		if err := mp.Shutdown(ctx); err != nil {
			otel.Handle(err)
		}
		return tp.Shutdown(ctx)
	}, nil
}

// newLocalProviders builds providers with no exporter or reader attached.
// Spans are still sampled and carry valid span contexts, so propagation works;
// they are simply never exported (Refs #12).
func newLocalProviders(res *resource.Resource) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))
	return tp, mp
}

// newExportingProviders builds providers that batch spans and periodically
// push metrics over OTLP gRPC (insecure) to endpoint.
func newExportingProviders(ctx context.Context, res *resource.Resource, endpoint string) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider, error) {
	traceExp, err := otlptrace.New(ctx,
		otlptracegrpc.NewClient(
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		// Shut the trace provider down so a failed metrics wire-up leaves no
		// half-initialized state behind.
		_ = tp.Shutdown(ctx)
		return nil, nil, fmt.Errorf("create otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	return tp, mp, nil
}
