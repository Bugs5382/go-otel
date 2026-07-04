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
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installManualReader swaps in a global MeterProvider backed by a ManualReader
// so the helpers under test record real measurements that can be collected and
// asserted without a live collector. It returns the reader and restores the
// previous global provider when the test ends.
func installManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return reader
}

// findMetric returns the collected aggregation for the named instrument.
func findMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not found in collected data", name)
	return metricdata.Metrics{}
}

func TestInitSetsMeterProvider(t *testing.T) {
	shutdown, err := Init(context.Background(), "test-service", "localhost:4317")
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Fatalf("expected SDK MeterProvider after Init, got %T", otel.GetMeterProvider())
	}
}

func TestCounterRecords(t *testing.T) {
	reader := installManualReader(t)

	c := Counter("test.requests", "count of test requests")
	c.Add(context.Background(), 3)

	m := findMetric(t, reader, "test.requests")
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", m.Data)
	}
	if got := len(sum.DataPoints); got != 1 {
		t.Fatalf("expected 1 data point, got %d", got)
	}
	if sum.DataPoints[0].Value != 3 {
		t.Fatalf("expected value 3, got %d", sum.DataPoints[0].Value)
	}
	if m.Description != "count of test requests" {
		t.Fatalf("unexpected description %q", m.Description)
	}
}

func TestHistogramRecords(t *testing.T) {
	reader := installManualReader(t)

	h := Histogram("test.latency", "latency of test op", "s")
	h.Record(context.Background(), 0.25)
	h.Record(context.Background(), 0.75)

	m := findMetric(t, reader, "test.latency")
	if m.Unit != "s" {
		t.Fatalf("expected unit \"s\", got %q", m.Unit)
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("expected Histogram[float64], got %T", m.Data)
	}
	if got := len(hist.DataPoints); got != 1 {
		t.Fatalf("expected 1 data point, got %d", got)
	}
	if hist.DataPoints[0].Count != 2 {
		t.Fatalf("expected count 2, got %d", hist.DataPoints[0].Count)
	}
}

func TestMetricsMiddlewareRecords(t *testing.T) {
	reader := installManualReader(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(Metrics(mux))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/items/42")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	// Duration histogram and request counter must both record for the request.
	dur := findMetric(t, reader, "http.server.request.duration")
	hist, ok := dur.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("expected Histogram[float64], got %T", dur.Data)
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("expected one recorded duration, got %+v", hist.DataPoints)
	}
	if dur.Unit != "s" {
		t.Fatalf("expected duration unit \"s\", got %q", dur.Unit)
	}

	// Route and status attributes must follow the HTTP semconv names.
	attrs := hist.DataPoints[0].Attributes
	if v, ok := attrs.Value("http.route"); !ok || v.AsString() != "GET /items/{id}" {
		t.Fatalf("expected templated http.route, got %v (present=%v)", v.AsString(), ok)
	}
	if v, ok := attrs.Value("http.response.status_code"); !ok || v.AsInt64() != http.StatusCreated {
		t.Fatalf("expected status_code 201, got %v (present=%v)", v.AsInt64(), ok)
	}
	if v, ok := attrs.Value("http.request.method"); !ok || v.AsString() != http.MethodGet {
		t.Fatalf("expected method GET, got %v (present=%v)", v.AsString(), ok)
	}

	count := findMetric(t, reader, "http.server.request.count")
	sum, ok := count.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("expected Sum[int64], got %T", count.Data)
	}
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("expected request count 1, got %+v", sum.DataPoints)
	}
}
