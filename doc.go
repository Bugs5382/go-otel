// Package otel wires OTLP gRPC trace and metric exporters into global Tracer
// and Meter providers from a single Init call, and provides convenience
// instrument constructors plus RED HTTP middleware. With an empty endpoint, Init installs the providers and the
// W3C propagator without any exporter, so trace context still propagates while
// spans and metrics are dropped.
//
// InitWithOptions is Init with options, such as WithInsecureSkipVerify for a
// collector with a self-signed certificate. ExportState reports the last
// success and last error of each signal, for health and status pages.
//
// WithLogs adds opt-in log export. LogWriter takes JSON log lines, such as
// zerolog's, and exports what its default-deny filters let through.
//
// WithTelemetryFilter turns on default-deny filters for everything else that
// leaves a service: span names, attributes, events, status text and trace state,
// metric attributes and exemplars, the resource, baggage, and the text of SDK
// errors. WithTraceAllowAttributes, WithMetricAllowAttributes and the other allow
// options name what may pass. Without these options nothing is filtered.
//
// Counter, Histogram, and Metrics return or accept raw go.opentelemetry.io/otel
// types and remain unchanged for existing callers. A consumer that wants to
// avoid any raw otel import in its own code should instead use:
//
//   - Attr and KV to build a neutral attribute (no attribute.KeyValue).
//   - NewCounter/NewHistogram, returning the neutral CounterMetric/
//     HistogramMetric interfaces (no metric.Int64Counter/Float64Histogram).
//   - GRPCServerStatsHandler/GRPCClientStatsHandler, returning
//     google.golang.org/grpc/stats.Handler (a gRPC transport type, not otel)
//     for grpc.StatsHandler/grpc.WithStatsHandler, so wiring gRPC
//     instrumentation never requires importing otelgrpc directly.
//
// The neutral interfaces are named CounterMetric/HistogramMetric rather than
// Counter/Histogram because those names are already taken by the raw-returning
// functions above, and Go does not allow a function and a type to share a name
// in one package.
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
