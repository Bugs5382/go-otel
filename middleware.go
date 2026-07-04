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
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics wraps an http.Handler and records the RED signals (request rate,
// errors, duration) for every request it serves, using OTel HTTP server
// semantic-convention instrument and attribute names:
//
//   - http.server.request.duration    histogram, seconds
//   - http.server.request.count       counter (rate and errors derive from it)
//
// Each measurement carries http.request.method, http.route, and
// http.response.status_code attributes. It depends only on net/http, so it
// composes with any stdlib-compatible router. Call Init first so the
// instruments record against the global MeterProvider.
func Metrics(next http.Handler) http.Handler {
	duration := Histogram(
		"http.server.request.duration",
		"Duration of inbound HTTP requests.",
		"s",
	)
	count := Counter(
		"http.server.request.count",
		"Count of inbound HTTP requests.",
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		attrs := metric.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route(r)),
			attribute.Int("http.response.status_code", sw.status),
		)
		duration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		count.Add(r.Context(), 1, attrs)
	})
}

// route reports a low-cardinality route label. It prefers the pattern matched
// by the net/http 1.22+ ServeMux (for example "GET /items/{id}") and falls back
// to the raw path when no pattern is set, keeping the caller free of any router
// dependency.
func route(r *http.Request) string {
	if p := r.Pattern; p != "" {
		return p
	}
	return r.URL.Path
}

// statusWriter captures the response status code so it can be attached as a
// metric attribute. It defaults to 200, matching net/http's behavior when a
// handler writes a body without calling WriteHeader.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}
