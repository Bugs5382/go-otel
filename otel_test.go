package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
