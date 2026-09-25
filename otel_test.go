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
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInitReturnsShutdownAndSetsPropagator(t *testing.T) {
	shutdown, err := Init(context.Background(), "test-service", "localhost:4317")
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	// Composite W3C TraceContext + Baggage propagator must be installed.
	prop := otel.GetTextMapPropagator()
	fields := prop.Fields()
	if !contains(fields, "traceparent") || !contains(fields, "baggage") {
		t.Fatalf("expected traceparent+baggage propagation, got %v", fields)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	_ = propagation.TraceContext{} // ensure import used
}

// With no endpoint, Init installs SDK providers with no exporter so spans still
// carry real trace context across process boundaries, and shutdown has nothing
// to flush, so it returns promptly without error.
func TestInitWithoutEndpointPropagatesAndDrops(t *testing.T) {
	for _, endpoint := range []string{"", "   "} {
		t.Run("endpoint="+strconv.Quote(endpoint), func(t *testing.T) {
			shutdown, err := Init(context.Background(), "test-service", endpoint)
			if err != nil {
				t.Fatalf("Init returned error: %v", err)
			}
			if shutdown == nil {
				t.Fatal("Init returned nil shutdown")
			}

			if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
				t.Fatalf("expected SDK tracer provider, got %T", otel.GetTracerProvider())
			}
			if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
				t.Fatalf("expected SDK meter provider, got %T", otel.GetMeterProvider())
			}

			// An incoming traceparent is continued, and the child's context is
			// injected on the way out.
			const parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
			in := propagation.MapCarrier{"traceparent": "00-" + parentTraceID + "-00f067aa0ba902b7-01"}
			prop := otel.GetTextMapPropagator()
			ctx := prop.Extract(context.Background(), in)
			ctx, span := otel.Tracer("test").Start(ctx, "op")
			if got := span.SpanContext().TraceID().String(); got != parentTraceID {
				t.Fatalf("child span trace ID = %s, want %s", got, parentTraceID)
			}
			out := propagation.MapCarrier{}
			prop.Inject(ctx, out)
			span.End()
			if !strings.Contains(out.Get("traceparent"), parentTraceID) {
				t.Fatalf("outgoing traceparent %q does not carry trace ID %s", out.Get("traceparent"), parentTraceID)
			}

			NewCounter("test.no_endpoint.count", "dropped").Add(context.Background(), 1)

			sctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if err := shutdown(sctx); err != nil {
				t.Fatalf("shutdown returned error: %v", err)
			}
		})
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
