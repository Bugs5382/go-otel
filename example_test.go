package otel_test

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
	"regexp"

	gootel "github.com/Bugs5382/go-otel"
	"google.golang.org/grpc"
)

// Example demonstrates the neutral metrics surface: a consumer records
// measurements through CounterMetric/HistogramMetric and tags them with Attr,
// without importing any go.opentelemetry.io/otel package itself.
func Example() {
	ctx := context.Background()

	orders := gootel.NewCounter("orders.placed", "Orders placed.")
	latency := gootel.NewHistogram("db.query.duration", "Query duration.", "s")

	orders.Add(ctx, 1, gootel.KV("outcome", "ok"), gootel.KV("region", "us-east"))
	latency.Record(ctx, 0.042, gootel.KV("outcome", "ok"))

	fmt.Println("recorded")
	// Output: recorded
}

// ExampleGRPCServerStatsHandler shows wiring gRPC server instrumentation
// without importing otelgrpc directly: GRPCServerStatsHandler returns a
// google.golang.org/grpc/stats.Handler that grpc.StatsHandler accepts as-is.
func ExampleGRPCServerStatsHandler() {
	_ = grpc.NewServer(grpc.StatsHandler(gootel.GRPCServerStatsHandler()))
	fmt.Println("server configured")
	// Output: server configured
}

// ExampleWithTelemetryFilter opts a service into the default-deny filters and
// names what may leave.
func ExampleWithTelemetryFilter() {
	ctx := context.Background()
	method := regexp.MustCompile(`^my\.package\.v1\.[A-Za-z]+/[A-Za-z]+$`)
	shutdown, err := gootel.InitWithOptions(ctx, "my-service", "",
		gootel.WithTelemetryFilter(true),
		gootel.WithTraceAllowSpanNames(method),
		gootel.WithTraceAllowAttributes("rpc.method"),
		gootel.WithMetricAllowAttributes("rpc.method", "http.route"),
		gootel.WithAttributeValuePattern("rpc.method", method),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = shutdown(ctx) }()

	fmt.Println("filtered")
	// Output: filtered
}
