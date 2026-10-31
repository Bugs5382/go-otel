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
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SignalStatus is the export history of one signal since Init. A zero time
// means that outcome has not happened yet. LastError stays in place after a
// later success, so compare LastSuccess with LastErrorAt to see which came
// last.
type SignalStatus struct {
	// LastSuccess is when an export last reached the collector.
	LastSuccess time.Time
	// LastErrorAt is when an export last failed.
	LastErrorAt time.Time
	// LastError describes that failure, with the endpoint password, any URL
	// userinfo and the OTLP header values removed.
	LastError string
}

// ExportStatus is what ExportState returns.
type ExportStatus struct {
	// Configured is true when the latest successful Init built exporters. It is
	// false before Init and after an Init with an empty endpoint, and the
	// signals are then zero.
	Configured bool
	// Traces is the span export history.
	Traces SignalStatus
	// Metrics is the metric export history.
	Metrics SignalStatus
}

// ExportState reports how exports have gone since the latest successful Init,
// separately for traces and metrics. It is safe for concurrent use. The history
// fills in on the exporters' own schedules, so right after Init both signals
// are zero. It keeps its last values after shutdown.
func ExportState() ExportStatus {
	t := activeTracker.Load()
	if t == nil {
		return ExportStatus{}
	}
	return ExportStatus{Configured: true, Traces: t.traces.status(), Metrics: t.metrics.status()}
}

// activeTracker follows Init the way the global providers do: the latest
// successful call replaces it, and nil means no exporters.
var activeTracker atomic.Pointer[exportTracker]

type exportTracker struct {
	redactor *strings.Replacer
	traces   signalTracker
	metrics  signalTracker
}

func newExportTracker(endpoint string) *exportTracker {
	return &exportTracker{redactor: newRedactor(endpoint)}
}

func (t *exportTracker) redact(err error) string {
	return urlUserinfo.ReplaceAllString(t.redactor.Replace(err.Error()), "$1")
}

type signalTracker struct {
	mu     sync.RWMutex
	latest SignalStatus
}

func (s *signalTracker) status() SignalStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latest
}

func (s *signalTracker) succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest.LastSuccess = time.Now()
}

func (s *signalTracker) failed(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest.LastErrorAt = time.Now()
	s.latest.LastError = msg
}

func (t *exportTracker) record(s *signalTracker, err error) {
	if err != nil {
		s.failed(t.redact(err))
		return
	}
	s.succeeded()
}

type trackedSpanExporter struct {
	sdktrace.SpanExporter
	tracker *exportTracker
}

func (e trackedSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	e.tracker.record(&e.tracker.traces, err)
	return err
}

type trackedMetricExporter struct {
	sdkmetric.Exporter
	tracker *exportTracker
}

func (e trackedMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	err := e.Exporter.Export(ctx, rm)
	e.tracker.record(&e.tracker.metrics, err)
	return err
}

const redactedValue = "[redacted]"

var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s"'@]+@`)

// newRedactor replaces the secrets this process holds for its exporters. A
// collector or proxy can echo any of them back in an error message, and that
// message outlives the call in ExportState.
func newRedactor(endpoint string) *strings.Replacer {
	var secrets []string
	if u, err := url.Parse(endpoint); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			secrets = append(secrets, pw)
		}
	}
	for _, name := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_HEADERS"} {
		secrets = append(secrets, headerValues(os.Getenv(name))...)
	}

	// Longest first, so a secret that contains another is removed whole.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	var pairs []string
	for _, s := range secrets {
		if s != "" {
			pairs = append(pairs, s, redactedValue)
		}
	}
	return strings.NewReplacer(pairs...)
}

// headerValues returns each value of a key=value,key=value OTLP header list,
// both as written and percent-decoded.
func headerValues(list string) []string {
	var values []string
	for pair := range strings.SplitSeq(list, ",") {
		_, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		values = append(values, value)
		if decoded, err := url.PathUnescape(value); err == nil {
			values = append(values, decoded)
		}
	}
	return values
}
