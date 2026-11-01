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
	"errors"
	"log"
	"regexp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// spanPlaceholder names a span whose own name isn't allowed.
	spanPlaceholder = "[span]"

	// exceptionEvent is what RecordError adds. It is never exported, because it
	// holds an error's text and stack.
	exceptionEvent = "exception"

	// unmatchedRoute and otherMethod stand in for what a client sent, in the
	// Metrics middleware. _OTHER is the semantic conventions' own value.
	unmatchedRoute = "unmatched"
	otherMethod    = "_OTHER"

	// metricCardinalityLimit is the SDK's own default, set explicitly so an
	// environment variable can't lift it.
	metricCardinalityLimit = 2000

	// errorReportInterval limits the redacted error line: a collector outage
	// fails every export, and the line must not become a load of its own.
	errorReportInterval = 30 * time.Second
)

// filterConfig holds the telemetry filter options. Everything is off by
// default, so Init behaves as it always has.
type filterConfig struct {
	traces      bool
	traceAttrs  []string
	traceEvents []string
	spanNames   []*regexp.Regexp

	metrics     bool
	metricAttrs []string

	resource      bool
	resourceAttrs []string

	values       map[string]*regexp.Regexp
	noBaggage    bool
	redactErrors bool
}

// WithTelemetryFilter turns on every default-deny filter at once: the trace,
// metric and resource filters, dropping baggage, and redacted SDK errors. With
// it off, which is the default, none of them applies unless its own option
// turns it on.
//
// A filter that is on lets through only what its allow options name, so with no
// allow options a span carries its ids, times, kind and status code and an
// unnamed placeholder for a name, a metric point carries no attributes, and the
// resource carries only service.name.
func WithTelemetryFilter(enabled bool) Option {
	return func(c *config) {
		c.filter.traces = enabled
		c.filter.metrics = enabled
		c.filter.resource = enabled
		c.filter.noBaggage = enabled
		c.filter.redactErrors = enabled
	}
}

// WithTraceAllowAttributes turns the trace filter on and names the attributes a
// span, event or link may carry. Every other attribute is dropped, and what
// stays goes through the redactor. Calls add up, and a call with no names turns
// the filter on with nothing allowed.
//
// The filter also strips exception events and their stacks, reduces the status
// to its code, clears trace state and instrumentation scope attributes, and
// stops inbound trace state at the propagator.
func WithTraceAllowAttributes(names ...string) Option {
	return func(c *config) {
		c.filter.traces = true
		c.filter.traceAttrs = append(c.filter.traceAttrs, names...)
	}
}

// WithTraceAllowEvents turns the trace filter on and names the span events that
// may be exported. Every other event is dropped. An exception event never is,
// even when named, because it carries an error's text and stack.
func WithTraceAllowEvents(names ...string) Option {
	return func(c *config) {
		c.filter.traces = true
		c.filter.traceEvents = append(c.filter.traceEvents, names...)
	}
}

// WithTraceAllowSpanNames turns the trace filter on and sets the span names that
// may be exported. A name is kept when any pattern matches it and the redactor
// leaves it alone. Every other span keeps its place in the trace under the name
// "[span]", because a name can hold values: otelpgx uses the whole SQL text and
// otelgrpc uses whatever method a client sent.
func WithTraceAllowSpanNames(patterns ...*regexp.Regexp) Option {
	return func(c *config) {
		c.filter.traces = true
		c.filter.spanNames = append(c.filter.spanNames, patterns...)
	}
}

// WithMetricAllowAttributes turns the metric filter on and names the attributes
// a data point may carry. Every other attribute is dropped and the points
// aggregate without it. An allowed attribute whose value the redactor would
// change is dropped too, so no series is ever renamed by redaction. Calls add
// up, and a call with no names turns the filter on with nothing allowed.
//
// The filter also turns exemplars off, whatever OTEL_METRICS_EXEMPLAR_FILTER
// says, because an exemplar would carry the attributes dropped here. It clears
// instrumentation scope attributes, holds each instrument to 2000 series, and
// makes the Metrics middleware report "unmatched" for a request no route matched
// and "_OTHER" for a method that isn't a standard one, instead of what the client
// sent.
func WithMetricAllowAttributes(names ...string) Option {
	return func(c *config) {
		c.filter.metrics = true
		c.filter.metricAttrs = append(c.filter.metricAttrs, names...)
	}
}

// WithResourceAllowAttributes turns the resource filter on and names the
// resource attributes that may leave, on top of service.name. It is applied to
// each OTLP request as it is sent, on traces, metrics and logs alike, because
// every SDK provider merges OTEL_RESOURCE_ATTRIBUTES back into the resource it
// is given. Values go through the redactor. Calls add up.
func WithResourceAllowAttributes(names ...string) Option {
	return func(c *config) {
		c.filter.resource = true
		c.filter.resourceAttrs = append(c.filter.resourceAttrs, names...)
	}
}

// WithAttributeValuePattern bounds the values an allowed attribute may have, on
// traces and metrics: a value pattern doesn't match drops the attribute. Use it
// for a name whose value comes from outside, such as rpc.method, which holds
// whatever method a client sent. A nil pattern matches nothing. It doesn't allow
// the attribute, and a later call for the same name replaces an earlier one.
func WithAttributeValuePattern(name string, pattern *regexp.Regexp) Option {
	return func(c *config) {
		if c.filter.values == nil {
			c.filter.values = make(map[string]*regexp.Regexp)
		}
		c.filter.values[name] = pattern
	}
}

// WithBaggage sets whether the global propagator carries W3C baggage. It does
// unless this is false or WithTelemetryFilter is on, in which case baggage
// neither enters from a request nor goes out on a call.
func WithBaggage(enabled bool) Option {
	return func(c *config) { c.filter.noBaggage = !enabled }
}

// WithRedactedErrors replaces the SDK's default error handler and internal
// logger when enabled is true, because both print raw text: a malformed
// OTEL_EXPORTER_OTLP_HEADERS entry, an endpoint URL with its password, or
// whatever a collector sent back. The replacements write through the standard
// log package, at most one line every 30 seconds, and print the gRPC code or the
// SDK's own message and never the error's text. ExportState still has the
// redacted text of the last export error.
//
// It sets the global error handler and logger, so it replaces any handler set
// before Init. Both stay in place after shutdown.
func WithRedactedErrors(enabled bool) Option {
	return func(c *config) { c.filter.redactErrors = enabled }
}

// attrFilter is the default-deny check shared by spans, data points and the
// resource.
type attrFilter struct {
	allow    map[string]bool
	values   map[string]*regexp.Regexp
	redactor *logRedactor
}

// keep returns an attribute as it may leave. ok is false when it must not.
func (f *attrFilter) keep(kv attribute.KeyValue) (attribute.KeyValue, bool) {
	if !f.allow[string(kv.Key)] {
		return kv, false
	}
	if pattern, bound := f.values[string(kv.Key)]; bound && (pattern == nil || !pattern.MatchString(kv.Value.String())) {
		return kv, false
	}
	switch kv.Value.Type() {
	case attribute.BOOL, attribute.INT64, attribute.FLOAT64,
		attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE:
		return kv, true
	case attribute.STRING:
		text, ok := f.scrub(kv.Value.AsString())
		return kv.Key.String(text), ok
	case attribute.STRINGSLICE:
		texts := slices.Clone(kv.Value.AsStringSlice())
		for i, text := range texts {
			scrubbed, ok := f.scrub(text)
			if !ok {
				return kv, false
			}
			texts[i] = scrubbed
		}
		return kv.Key.StringSlice(texts), true
	}
	// Bytes, maps and nested values have no shape the redactor can read.
	return kv, false
}

func (f *attrFilter) scrub(text string) (string, bool) {
	if len(text) > maxLogLine {
		return "", false
	}
	return f.redactor.scrub(text)
}

func (f *attrFilter) attributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	kept := make([]attribute.KeyValue, 0, len(attrs))
	for _, kv := range attrs {
		if out, ok := f.keep(kv); ok {
			kept = append(kept, out)
		}
	}
	return kept
}

// metricAttribute is a view's attribute filter. It can only keep or drop, and
// dropping lets the points aggregate, where rewriting a value would give two
// series the same attributes.
func (f *attrFilter) metricAttribute(kv attribute.KeyValue) bool {
	out, ok := f.keep(kv)
	return ok && out.Value.String() == kv.Value.String()
}

type traceFilter struct {
	attrs  *attrFilter
	events map[string]bool
	names  []*regexp.Regexp
}

func (f *traceFilter) spanName(name string) string {
	for _, pattern := range f.names {
		if pattern == nil || !pattern.MatchString(name) {
			continue
		}
		if text, ok := f.attrs.scrub(name); ok && text == name {
			return name
		}
		break
	}
	return spanPlaceholder
}

func (f *traceFilter) spanEvents(events []sdktrace.Event) []sdktrace.Event {
	kept := make([]sdktrace.Event, 0, len(events))
	for _, event := range events {
		if event.Name != exceptionEvent && f.events[event.Name] {
			event.Attributes = f.attrs.attributes(event.Attributes)
			kept = append(kept, event)
		}
	}
	return kept
}

func (f *traceFilter) spanLinks(links []sdktrace.Link) []sdktrace.Link {
	kept := make([]sdktrace.Link, len(links))
	for i, link := range links {
		link.SpanContext = link.SpanContext.WithTraceState(trace.TraceState{})
		link.Attributes = f.attrs.attributes(link.Attributes)
		kept[i] = link
	}
	return kept
}

func (f *traceFilter) span(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	scope := s.InstrumentationScope()
	return filteredSpan{
		ReadOnlySpan: s,
		name:         f.spanName(s.Name()),
		context:      s.SpanContext().WithTraceState(trace.TraceState{}),
		attrs:        f.attrs.attributes(s.Attributes()),
		events:       f.spanEvents(s.Events()),
		links:        f.spanLinks(s.Links()),
		status:       sdktrace.Status{Code: s.Status().Code},
		scope:        instrumentation.Scope{Name: scope.Name, Version: scope.Version, SchemaURL: scope.SchemaURL},
	}
}

// filteredSpan overrides everything of a span that can hold text. Its ids,
// times, kind and counts pass through from the span it embeds.
type filteredSpan struct {
	sdktrace.ReadOnlySpan
	name    string
	context trace.SpanContext
	attrs   []attribute.KeyValue
	events  []sdktrace.Event
	links   []sdktrace.Link
	status  sdktrace.Status
	scope   instrumentation.Scope
}

func (s filteredSpan) Name() string                                { return s.name }
func (s filteredSpan) SpanContext() trace.SpanContext              { return s.context }
func (s filteredSpan) Attributes() []attribute.KeyValue            { return s.attrs }
func (s filteredSpan) Events() []sdktrace.Event                    { return s.events }
func (s filteredSpan) Links() []sdktrace.Link                      { return s.links }
func (s filteredSpan) Status() sdktrace.Status                     { return s.status }
func (s filteredSpan) InstrumentationScope() instrumentation.Scope { return s.scope }

type filteredSpanExporter struct {
	sdktrace.SpanExporter
	filter *traceFilter
}

func (e filteredSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	filtered := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		filtered[i] = e.filter.span(span)
	}
	return e.SpanExporter.ExportSpans(ctx, filtered)
}

// filteredMetricExporter clears the scope attributes a view can't reach.
type filteredMetricExporter struct {
	sdkmetric.Exporter
}

func (e filteredMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	for i := range rm.ScopeMetrics {
		rm.ScopeMetrics[i].Scope.Attributes = attribute.Set{}
	}
	return e.Exporter.Export(ctx, rm)
}

// resourceAttributes filters the attributes of one OTLP resource.
func (f *attrFilter) resourceAttributes(res *resourcepb.Resource) {
	if res == nil {
		return
	}
	kept := make([]*commonpb.KeyValue, 0, len(res.Attributes))
	for _, kv := range res.Attributes {
		if !f.allow[kv.Key] {
			continue
		}
		switch value := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			text, ok := f.scrub(value.StringValue)
			if !ok {
				continue
			}
			value.StringValue = text
		case *commonpb.AnyValue_BoolValue, *commonpb.AnyValue_IntValue, *commonpb.AnyValue_DoubleValue:
		default:
			continue
		}
		kept = append(kept, kv)
	}
	res.Attributes = kept
}

// interceptor filters the resource at the last step before the wire. It can't
// be done earlier: each provider's WithResource merges OTEL_RESOURCE_ATTRIBUTES
// back in, and a log record offers no way to change its resource.
func (f *attrFilter) interceptor(ctx context.Context, method string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	switch request := req.(type) {
	case *coltracepb.ExportTraceServiceRequest:
		for _, spans := range request.GetResourceSpans() {
			f.resourceAttributes(spans.GetResource())
		}
	case *colmetricpb.ExportMetricsServiceRequest:
		for _, metrics := range request.GetResourceMetrics() {
			f.resourceAttributes(metrics.GetResource())
		}
	case *collogspb.ExportLogsServiceRequest:
		for _, logs := range request.GetResourceLogs() {
			f.resourceAttributes(logs.GetResource())
		}
	}
	return invoke(ctx, method, req, reply, conn, opts...)
}

// filters are the filters one Init built. A nil field is a filter that is off.
type filters struct {
	traces   *traceFilter
	metrics  *attrFilter
	resource *attrFilter
}

func newFilters(cfg config) filters {
	redactor := newLogRedactor(cfg.logs.redactValues, cfg.logs.redactPatterns)
	attrs := func(names ...string) *attrFilter {
		return &attrFilter{allow: nameSet(names), values: cfg.filter.values, redactor: redactor}
	}
	var f filters
	if cfg.filter.traces {
		f.traces = &traceFilter{attrs: attrs(cfg.filter.traceAttrs...), events: nameSet(cfg.filter.traceEvents), names: cfg.filter.spanNames}
	}
	if cfg.filter.metrics {
		f.metrics = attrs(cfg.filter.metricAttrs...)
	}
	if cfg.filter.resource {
		f.resource = attrs(append(cfg.filter.resourceAttrs, "service.name")...)
	}
	return f
}

// metricsFiltered follows Init the way the global providers do, for the Metrics
// middleware, which is built without a reference to any Init.
var metricsFiltered atomic.Bool

// noTraceState drops inbound trace state, which is text a caller chose. The
// SDK would otherwise put it on every span and forward it on every call.
type noTraceState struct {
	propagation.TraceContext
}

func (p noTraceState) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	ctx = p.TraceContext.Extract(ctx, carrier)
	remote := trace.SpanContextFromContext(ctx)
	if remote.TraceState().Len() == 0 {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, remote.WithTraceState(trace.TraceState{}))
}

func newPropagator(cfg filterConfig) propagation.TextMapPropagator {
	var parts []propagation.TextMapPropagator
	if cfg.traces {
		parts = append(parts, noTraceState{})
	} else {
		parts = append(parts, propagation.TraceContext{})
	}
	if !cfg.noBaggage {
		parts = append(parts, propagation.Baggage{})
	}
	return propagation.NewCompositeTextMapPropagator(parts...)
}

// errorReporter is the SDK's error handler when errors are redacted. Only the
// code leaves: error text can hold anything, including a malformed setting with
// nothing around it to say it's secret.
type errorReporter struct {
	next atomic.Int64
}

func (r *errorReporter) report(text string) {
	now := time.Now()
	next := r.next.Load()
	if now.UnixNano() < next || !r.next.CompareAndSwap(next, now.Add(errorReportInterval).UnixNano()) {
		return
	}
	log.Print("go-otel: " + text)
}

func (r *errorReporter) Handle(err error) {
	r.report("telemetry error, code " + errorCode(err))
}

func errorCode(err error) string {
	code := status.Code(err)
	switch {
	case code != codes.Unknown:
		return code.String()
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded.String()
	case errors.Is(err, context.Canceled):
		return codes.Canceled.String()
	}
	return code.String()
}

// sdkLogSink is the SDK's internal logger when errors are redacted. It is
// silent below error level, where the SDK dumps whole metric batches, and it
// never prints the error or its key-value pairs, where the SDK puts the raw
// header or URL. The message is a constant in the SDK's code.
type sdkLogSink struct {
	reporter *errorReporter
}

func (sdkLogSink) Init(logr.RuntimeInfo)            {}
func (sdkLogSink) Enabled(int) bool                 { return false }
func (sdkLogSink) Info(int, string, ...any)         {}
func (s sdkLogSink) WithValues(...any) logr.LogSink { return s }
func (s sdkLogSink) WithName(string) logr.LogSink   { return s }

func (s sdkLogSink) Error(_ error, msg string, _ ...any) {
	s.reporter.report("telemetry error: " + msg)
}

func installRedactedErrors() {
	reporter := &errorReporter{}
	otel.SetErrorHandler(reporter)
	otel.SetLogger(logr.New(sdkLogSink{reporter: reporter}))
}
