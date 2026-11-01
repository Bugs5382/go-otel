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
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
)

// Init configures global Tracer and Meter providers that export over OTLP gRPC
// to otlpEndpoint, tagged with service.name=service. otlpEndpoint is a bare
// host:port (insecure) or an http or https URL (plaintext or TLS, by scheme).
// Traces and metrics ride the same endpoint and share one resource, so a single
// call wires both. The returned shutdown func flushes and closes both
// providers; callers should defer it.
//
// An empty (or all-whitespace) otlpEndpoint runs Init without a collector: the
// providers and the W3C propagator are installed as usual, so spans get real
// trace IDs and trace context still flows across process boundaries, but no
// exporter is built and spans and metrics are dropped. This suits local
// development, tests, and environments with no collector. Pass
// os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") to make export opt-in by
// environment.
func Init(ctx context.Context, service, otlpEndpoint string) (shutdown func(context.Context) error, err error) {
	return InitWithOptions(ctx, service, otlpEndpoint)
}

// Option changes how InitWithOptions sets up the exporters.
type Option func(*config)

type config struct {
	insecureSkipVerify bool
	logs               logConfig
}

// WithInsecureSkipVerify turns off certificate validation on an https endpoint
// when skip is true, for a collector with a private or self-signed certificate.
// The connection stays encrypted but the collector is not authenticated, so
// anyone who can intercept it can read the telemetry and the OTLP headers. It
// has no effect on a plaintext endpoint, except that logs stay off whenever it
// is on (see WithLogs).
func WithInsecureSkipVerify(skip bool) Option {
	return func(c *config) { c.insecureSkipVerify = skip }
}

// InitWithOptions is Init with options. With no options the two are the same.
func InitWithOptions(ctx context.Context, service, otlpEndpoint string, opts ...Option) (shutdown func(context.Context) error, err error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}

	res, err := newResource(service)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	var tp *sdktrace.TracerProvider
	var mp *sdkmetric.MeterProvider
	var lp *sdklog.LoggerProvider
	var emitter *logEmitter
	var tracker *exportTracker
	if strings.TrimSpace(otlpEndpoint) == "" {
		tp, mp = newLocalProviders(res)
	} else {
		tracker = newExportTracker(otlpEndpoint, cfg)
		tp, mp, lp, err = newExportingProviders(ctx, res, otlpEndpoint, cfg, tracker)
		if err != nil {
			return nil, err
		}
		if lp != nil {
			emitter = newLogEmitter(lp, cfg.logs)
		}
	}
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	activeTracker.Store(tracker)
	activeLogs.Store(emitter)

	// Install a global text-map propagator so W3C trace context and baggage
	// flow across process boundaries. Without this, injected/extracted carriers
	// are no-ops and traces break at every hop.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		// Stop taking lines first, then flush, so a line can't land in a queue
		// nothing will drain. A failed flush is the same environmental
		// condition as the metric one below.
		if lp != nil {
			activeLogs.CompareAndSwap(emitter, nil)
			if err := lp.Shutdown(ctx); err != nil {
				otel.Handle(err)
			}
		}

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

// newResource layers service.name over the SDK's default resource. The service
// part is schemaless, so the merge keeps the SDK default's schema URL instead
// of pinning a semconv version of our own: resource.Merge rejects two
// different non-empty schema URLs, and pinning one broke Init as soon as the
// SDK moved to a newer semconv (Refs #20).
func newResource(service string) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewSchemaless(attribute.String("service.name", service)),
	)
}

// newLocalProviders builds providers with no exporter or reader attached.
// Spans are still sampled and carry valid span contexts, so propagation works;
// they are simply never exported (Refs #12).
func newLocalProviders(res *resource.Resource) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res))
	return tp, mp
}

// otlpOptions are the exporter options for one endpoint, per signal.
type otlpOptions struct {
	trace  []otlptracegrpc.Option
	metric []otlpmetricgrpc.Option
	log    []otlploggrpc.Option
}

// exporterOptions turns endpoint into the exporter options. A bare host:port is
// insecure. A URL follows its scheme: http is plaintext and https is TLS. The
// SDK's WithEndpointURL only logs a bad URL and falls back to its default
// collector, so a bad one is rejected here, without repeating the URL's
// password in the error.
func exporterOptions(endpoint string, cfg config) (otlpOptions, error) {
	if !strings.Contains(endpoint, "://") {
		return otlpOptions{
			trace:  []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure()},
			metric: []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint), otlpmetricgrpc.WithInsecure()},
			log:    []otlploggrpc.Option{otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure()},
		}, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return otlpOptions{}, fmt.Errorf("parse otlp endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return otlpOptions{}, fmt.Errorf("otlp endpoint scheme %q: want http or https", u.Scheme)
	}
	if u.Host == "" {
		return otlpOptions{}, fmt.Errorf("otlp endpoint %q has no host", u.Redacted())
	}
	opts := otlpOptions{
		trace:  []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(endpoint)},
		metric: []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(endpoint)},
		log:    []otlploggrpc.Option{otlploggrpc.WithEndpointURL(endpoint)},
	}
	if u.Scheme == "https" && cfg.insecureSkipVerify {
		creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
		opts.trace = append(opts.trace, otlptracegrpc.WithTLSCredentials(creds))
		opts.metric = append(opts.metric, otlpmetricgrpc.WithTLSCredentials(creds))
	}
	return opts, nil
}

// newExportingProviders builds providers that batch spans and periodically
// push metrics over OTLP gRPC to endpoint, reporting each export to tracker.
// The logger provider is nil unless tracker says logs export.
func newExportingProviders(ctx context.Context, res *resource.Resource, endpoint string, cfg config, tracker *exportTracker) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider, *sdklog.LoggerProvider, error) {
	opts, err := exporterOptions(endpoint, cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	traceExp, err := otlptrace.New(ctx, otlptracegrpc.NewClient(opts.trace...))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(trackedSpanExporter{SpanExporter: traceExp, tracker: tracker}),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetricgrpc.New(ctx, opts.metric...)
	if err != nil {
		// Shut the trace provider down so a failed metrics wire-up leaves no
		// half-initialized state behind.
		_ = tp.Shutdown(ctx)
		return nil, nil, nil, fmt.Errorf("create otlp metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(trackedMetricExporter{Exporter: metricExp, tracker: tracker})),
		sdkmetric.WithResource(res),
	)
	if tracker.logsOff != "" {
		return tp, mp, nil, nil
	}

	logExp, err := otlploggrpc.New(ctx, opts.log...)
	if err != nil {
		_ = mp.Shutdown(ctx)
		_ = tp.Shutdown(ctx)
		return nil, nil, nil, fmt.Errorf("create otlp log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(trackedLogExporter{Exporter: logExp, tracker: tracker})),
		sdklog.WithResource(res),
	)
	return tp, mp, lp, nil
}
