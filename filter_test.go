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
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// wire is everything a collector received, as text, plus what a downstream
// service received, so a canary can't hide in a field a test forgot to read.
type wire struct {
	traces, metrics, logs, downstream string
}

func (w wire) String() string {
	return strings.Join([]string{w.traces, w.metrics, w.logs, w.downstream}, "\n")
}

// resetTelemetry puts back what a filtered Init leaves global.
func resetTelemetry(t *testing.T) {
	t.Helper()
	handler := otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetErrorHandler(handler)
		if _, err := Init(context.Background(), "reset", ""); err != nil {
			t.Errorf("reset Init: %v", err)
		}
	})
}

// capture runs emit between a fresh Init and its shutdown.
func capture(t *testing.T, opts []Option, emit func() string) wire {
	t.Helper()
	resetTelemetry(t)
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	shutdown, err := InitWithOptions(context.Background(), "test-service", c.addr, opts...)
	if err != nil {
		t.Fatalf("InitWithOptions returned error: %v", err)
	}
	var downstream string
	if emit != nil {
		downstream = emit()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	_, logs := c.exported()
	c.wireMu.Lock()
	defer c.wireMu.Unlock()
	return wire{traces: c.traceWire.String(), metrics: c.metricWire.String(), logs: logs, downstream: downstream}
}

// healthServer fails every check the way a service does: a status message with
// an input value in it, after a recovered panic is recorded on the span.
type healthServer struct {
	healthpb.UnimplementedHealthServer
}

func (healthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(errors.New("panic: CANARY-EXCEPTION"), trace.WithStackTrace(true))
	span.SetStatus(codes.Error, "panic recovered")
	return nil, status.Error(grpccodes.Internal, "CANARY-GRPC-STATUS")
}

// grpcPair starts an instrumented server and returns a client for it, and the
// metadata of the last call the server received.
func grpcPair(t *testing.T) (*grpc.ClientConn, func() string) {
	t.Helper()
	var mu sync.Mutex
	var last string
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.StatsHandler(GRPCServerStatsHandler()),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			mu.Lock()
			last = fmt.Sprint(md)
			mu.Unlock()
			return next(ctx, req)
		}))
	healthpb.RegisterHealthServer(srv, healthServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(GRPCClientStatsHandler()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// emitEverywhere sends a canary down every path the SDK and its instrumentation
// have, and returns what a downstream service received.
func emitEverywhere(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, received := grpcPair(t)
	client := healthpb.NewHealthClient(conn)
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})
	_ = conn.Invoke(ctx, "/CANARY.Unknown/CANARY-RPC-METHOD", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = client.Check(r.Context(), &healthpb.HealthCheckRequest{})
		w.WriteHeader(http.StatusInternalServerError)
	})
	web := httptest.NewServer(otelhttp.NewHandler(Metrics(mux), "gateway"))
	defer web.Close()

	req, err := http.NewRequest(http.MethodGet, web.URL+"/items/CANARY-URL-PATH?token=CANARY-URL-QUERY", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "CANARY-HOST-HEADER"
	req.Header.Set("User-Agent", "CANARY-USER-AGENT")
	req.Header.Set("X-Forwarded-For", "CANARY-FORWARDED-FOR")
	req.Header.Set("Authorization", "Bearer CANARY-AUTHORIZATION")
	req.Header.Set("Cookie", "session=CANARY-COOKIE")
	req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	req.Header.Set("tracestate", "vendor=CANARY-TRACE-STATE")
	req.Header.Set("baggage", "user=CANARY-BAGGAGE")
	drain(t, req)
	if req, err = http.NewRequest(http.MethodGet, web.URL+"/nothing/CANARY-RAW-PATH", nil); err != nil {
		t.Fatalf("new request: %v", err)
	}
	drain(t, req)
	if req, err = http.NewRequest("CANARY-METHOD", web.URL+"/x", nil); err != nil {
		t.Fatalf("new request: %v", err)
	}
	drain(t, req)

	tracer := otel.Tracer("sweep", trace.WithInstrumentationAttributes(attribute.String("scope", "CANARY-TRACE-SCOPE")))
	spanCtx, span := tracer.Start(ctx, "CANARY-SPAN-NAME", trace.WithLinks(trace.Link{
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}}),
		Attributes:  []attribute.KeyValue{attribute.String("link", "CANARY-LINK-ATTR")},
	}))
	span.SetAttributes(attribute.String("detail", "CANARY-SPAN-ATTR"))
	span.AddEvent("CANARY-EVENT-NAME", trace.WithAttributes(attribute.String("detail", "CANARY-EVENT-ATTR")))
	span.SetStatus(codes.Error, "CANARY-STATUS-TEXT")

	meter := otel.Meter("sweep", metric.WithInstrumentationAttributes(attribute.String("scope", "CANARY-METRIC-SCOPE")))
	histogram, err := meter.Float64Histogram("sweep.duration")
	if err != nil {
		t.Fatalf("histogram: %v", err)
	}
	histogram.Record(spanCtx, 1, metric.WithAttributes(attribute.String("detail", "CANARY-METRIC-ATTR")))
	span.End()

	write(t, jsonLine("info", "ok", ""))
	return received()
}

func drain(t *testing.T, req *http.Request) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func setResourceCanaries(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.token=CANARY-RESOURCE,host.name=CANARY-HOST-NAME")
	t.Setenv("OTEL_SERVICE_NAME", "CANARY-SERVICE-NAME")
}

func wantContains(t *testing.T, signal, text string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if !strings.Contains(text, canary) {
			t.Errorf("%s: %s missing", signal, canary)
		}
	}
}

func wantAbsent(t *testing.T, signal, text string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(text, canary) {
			t.Errorf("%s: %s leaked: %s", signal, canary, around(text, canary))
		}
	}
}

func around(text, canary string) string {
	at := strings.Index(text, canary)
	return text[max(0, at-80):min(len(text), at+len(canary)+40)]
}

// Without the filters every canary really gets out, so the filtered run below
// passing means the filters stopped them and not that the path was dead. It
// also pins that nothing changes for a service that doesn't opt in.
func TestWithoutFilterEveryPathExports(t *testing.T) {
	setResourceCanaries(t)
	got := capture(t, []Option{WithLogs(true)}, func() string { return emitEverywhere(t) })

	wantContains(t, "traces", got.traces,
		"CANARY-EXCEPTION", "exception.stacktrace", "CANARY-GRPC-STATUS", "CANARY-RPC-METHOD", "CANARY-URL-PATH",
		"CANARY-HOST-HEADER", "CANARY-USER-AGENT", "CANARY-FORWARDED-FOR", "CANARY-TRACE-STATE", "CANARY-METHOD",
		"CANARY-SPAN-NAME", "CANARY-SPAN-ATTR", "CANARY-EVENT-NAME", "CANARY-EVENT-ATTR", "CANARY-LINK-ATTR",
		"CANARY-STATUS-TEXT", "CANARY-TRACE-SCOPE", "CANARY-RESOURCE", "CANARY-HOST-NAME")
	wantContains(t, "metrics", got.metrics,
		"CANARY-RPC-METHOD", "CANARY-HOST-HEADER", "CANARY-RAW-PATH", "CANARY-METHOD", "CANARY-METRIC-ATTR",
		"CANARY-METRIC-SCOPE", "CANARY-RESOURCE", "exemplars:")
	wantContains(t, "logs", got.logs, "CANARY-RESOURCE")
	wantContains(t, "downstream", got.downstream, "CANARY-BAGGAGE", "CANARY-TRACE-STATE")
}

func TestTelemetryFilterStopsEveryCanary(t *testing.T) {
	setResourceCanaries(t)
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "always_on")
	t.Setenv("OTEL_SEMCONV_STABILITY_OPT_IN", "rpc/dup")
	t.Setenv("OTEL_GO_X_RESOURCE", "true")
	got := capture(t, []Option{WithTelemetryFilter(true), WithLogs(true)}, func() string { return emitEverywhere(t) })

	wantContains(t, "traces", got.traces, "start_time_unix_nano", `name:"[span]"`, "STATUS_CODE_ERROR", `"service.name"`)
	wantContains(t, "metrics", got.metrics, "http.server.request.count", "sweep.duration", `"service.name"`)
	wantContains(t, "logs", got.logs, `"service.name"`)
	wantContains(t, "downstream", got.downstream, "traceparent")
	wantAbsent(t, "wire", got.String(), "CANARY", "exception", "exemplars:", "baggage", "tracestate", "telemetry.sdk")
}

func TestTraceFilterAllowLists(t *testing.T) {
	secretKey := strings.TrimSuffix(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("key")})), "\n")
	secretKey = secretKey[:strings.LastIndex(secretKey, "\n")]
	opts := []Option{
		WithTraceAllowAttributes("route", "password_hint", "count", "ok", "ratio", "tags", "ids", "pem", "key_list", "blob", "nested", "too_long", "bound"),
		WithTraceAllowEvents("retry", "exception"),
		WithTraceAllowSpanNames(regexp.MustCompile(`^checkout$`), nil, regexp.MustCompile(`^login`)),
		WithAttributeValuePattern("bound", regexp.MustCompile(`^safe$`)),
		WithLogRedactPatterns(regexp.MustCompile(`tok_[a-z0-9]+`)),
		WithLogRedactValues("CANARY-EXACT-VALUE"),
	}
	got := capture(t, opts, func() string {
		tracer := otel.Tracer("filter")
		parent := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{7}, SpanID: trace.SpanID{7}, TraceFlags: trace.FlagsSampled, Remote: true,
			TraceState: mustTraceState(t, "vendor=CANARY-STATE"),
		})
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)
		_, span := tracer.Start(ctx, "checkout", trace.WithLinks(trace.Link{
			SpanContext: parent,
			Attributes:  []attribute.KeyValue{attribute.String("route", "GET /link"), attribute.String("other", "CANARY-LINK")},
		}))
		span.SetAttributes(
			attribute.String("route", "GET /items/{id}"),
			attribute.String("password_hint", "password=CANARY-KEYED tok_abc123 CANARY-EXACT-VALUE"),
			attribute.Int("count", 3),
			attribute.Bool("ok", true),
			attribute.Float64("ratio", 0.5),
			attribute.StringSlice("tags", []string{"plain", "token=CANARY-SLICE"}),
			attribute.Int64Slice("ids", []int64{1, 2}),
			attribute.String("pem", secretKey),
			attribute.StringSlice("key_list", []string{secretKey}),
			attribute.ByteSlice("blob", []byte("password=CANARY-BYTES")),
			attribute.Map("nested", attribute.String("inner", "password=CANARY-MAP")),
			attribute.String("too_long", strings.Repeat("x", maxLogLine+1)+"CANARY-LONG"),
			attribute.String("not_allowed", "CANARY-NOT-ALLOWED"),
			attribute.String("bound", "CANARY-BOUND"),
		)
		span.AddEvent("retry", trace.WithAttributes(attribute.Int("count", 2), attribute.String("other", "CANARY-EVENT")))
		span.AddEvent("exception", trace.WithAttributes(attribute.String("route", "CANARY-EXCEPTION-ROUTE")))
		span.AddEvent("not_allowed")
		span.RecordError(errors.New("CANARY-RECORDED"), trace.WithStackTrace(true))
		span.SetStatus(codes.Error, "token=CANARY-STATUS")
		span.End()

		_, other := tracer.Start(context.Background(), "password=CANARY-NAME")
		other.End()
		_, second := tracer.Start(context.Background(), "login token=CANARY-MATCHED-NAME")
		second.End()
		_, ok := tracer.Start(context.Background(), "checkout")
		ok.SetStatus(codes.Ok, "")
		ok.End()
		return ""
	})

	wantContains(t, "traces", got.traces,
		`name:"checkout"`, `name:"[span]"`, `name:"retry"`, `"GET /items/{id}"`, `"GET /link"`,
		`password=[redacted]`, `int_value:3`, `bool_value:true`, `double_value:0.5`, `"plain"`, `int_value:2`,
		"STATUS_CODE_ERROR", "STATUS_CODE_OK", "parent_span_id", "attributes:", `key:"tags"`, `key:"count"`)
	wantAbsent(t, "traces", got.traces,
		"CANARY", `name:"exception"`, `name:"not_allowed"`, "tok_abc123", `key:"pem"`, `key:"key_list"`, `key:"blob"`, `key:"nested"`,
		`key:"too_long"`, `key:"bound"`, "trace_state", "exception.")
	if strings.Contains(got.traces, "message:") {
		t.Errorf("status text left: %s", around(got.traces, "message:"))
	}
}

// The OTLP exporter doesn't send a link's trace state today, so nothing on the
// wire can show this, but the next SDK could.
func TestTraceFilterClearsLinkTraceState(t *testing.T) {
	link := sdktrace.Link{SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{7}, SpanID: trace.SpanID{7}, TraceState: mustTraceState(t, "vendor=CANARY-STATE"),
	})}
	f := newFilters(config{filter: filterConfig{traces: true}}).traces
	got := f.spanLinks([]sdktrace.Link{link})
	if got[0].SpanContext.TraceState().Len() != 0 {
		t.Fatalf("trace state kept: %v", got[0].SpanContext.TraceState())
	}
}

func mustTraceState(t *testing.T, s string) trace.TraceState {
	t.Helper()
	state, err := trace.ParseTraceState(s)
	if err != nil {
		t.Fatalf("parse trace state: %v", err)
	}
	return state
}

func TestTraceFilterKeepsTheTraceTogether(t *testing.T) {
	got := capture(t, []Option{WithTraceAllowAttributes()}, func() string {
		tracer := otel.Tracer("filter")
		ctx, parent := tracer.Start(context.Background(), "a")
		_, child := tracer.Start(ctx, "b", trace.WithSpanKind(trace.SpanKindClient))
		child.End()
		parent.End()
		return ""
	})
	wantContains(t, "traces", got.traces, "trace_id", "span_id", "parent_span_id", "SPAN_KIND_CLIENT", "SPAN_KIND_INTERNAL")
}

// otelgrpc names the span after whatever method the client sent, on the server
// too, so a bound on rpc.method has to hold on both traces and metrics.
func TestAttributeValuePatternBoundsClientValues(t *testing.T) {
	known := regexp.MustCompile(`^grpc\.health\.v1\.Health/Check$`)
	got := capture(t, []Option{
		WithTraceAllowAttributes("rpc.method"),
		WithTraceAllowSpanNames(known),
		WithMetricAllowAttributes("rpc.method"),
		WithAttributeValuePattern("rpc.method", known),
	}, func() string {
		conn, _ := grpcPair(t)
		_, _ = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
		_ = conn.Invoke(context.Background(), "/CANARY.Unknown/Method", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
		return ""
	})
	wantContains(t, "traces", got.traces, `name:"grpc.health.v1.Health/Check"`, `"rpc.method"`)
	wantContains(t, "metrics", got.metrics, `"rpc.method"`, `grpc.health.v1.Health/Check`)
	wantAbsent(t, "wire", got.String(), "CANARY")
}

func TestMetricFilterAllowLists(t *testing.T) {
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "always_on")
	got := capture(t, []Option{
		WithMetricAllowAttributes("outcome", "code", "secretish", "tags", "blob"),
		WithLogRedactValues("CANARY-EXACT-VALUE"),
	}, func() string {
		ctx, span := otel.Tracer("filter").Start(context.Background(), "a")
		defer span.End()
		NewCounter("filter.count", "Count.").Add(ctx, 1,
			KV("outcome", "ok"), KV("code", 7), KV("dropped", "CANARY-DROPPED"),
			KV("secretish", "token=CANARY-SECRET"), KV("tags", []string{"password=CANARY-SLICE"}))
		NewCounter("filter.count", "Count.").Add(ctx, 1,
			KV("outcome", "ok"), KV("code", 7), KV("dropped", "CANARY-OTHER"),
			KV("secretish", "token=CANARY-OTHER-SECRET"))
		NewCounter("filter.count", "Count.").Add(ctx, 1, KV("outcome", "CANARY-EXACT-VALUE"))
		return ""
	})
	wantContains(t, "metrics", got.metrics, "filter.count", `"outcome"`, `string_value:"ok"`, `key:"code"`, "as_int:2")
	wantAbsent(t, "metrics", got.metrics, "CANARY", "exemplars:", `key:"dropped"`, `key:"secretish"`, `key:"tags"`, "[redacted]")
}

func TestMetricsMiddlewareReportsNoRawInputWhenFiltered(t *testing.T) {
	serve := func() string {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) {})
		web := httptest.NewServer(Metrics(mux))
		defer web.Close()
		for _, path := range []string{"/items/CANARY-ITEM", "/nothing/CANARY-RAW-PATH"} {
			req, err := http.NewRequest(http.MethodGet, web.URL+path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			drain(t, req)
		}
		req, err := http.NewRequest("CANARY-METHOD", web.URL+"/x", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		drain(t, req)
		return ""
	}

	got := capture(t, []Option{WithMetricAllowAttributes("http.route", "http.request.method")}, serve)
	wantContains(t, "metrics", got.metrics, `"GET /items/{id}"`, `string_value:"unmatched"`, `string_value:"_OTHER"`, `string_value:"GET"`)
	wantAbsent(t, "metrics", got.metrics, "CANARY")

	got = capture(t, nil, serve)
	wantContains(t, "metrics", got.metrics, "CANARY-RAW-PATH", "CANARY-METHOD")
}

func TestResourceFilterHoldsOnEverySignal(t *testing.T) {
	setResourceCanaries(t)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.token=CANARY-RESOURCE,deployment.environment=dev,region=token=CANARY-VALUE")
	got := capture(t, []Option{
		WithResourceAllowAttributes("deployment.environment", "region"),
		WithLogs(true),
	}, func() string {
		_, span := otel.Tracer("filter").Start(context.Background(), "a")
		span.End()
		NewCounter("filter.count", "Count.").Add(context.Background(), 1)
		write(t, jsonLine("info", "ok", ""))
		return ""
	})
	for signal, text := range map[string]string{"traces": got.traces, "metrics": got.metrics, "logs": got.logs} {
		wantContains(t, signal, text, `"service.name"`, `"test-service"`, `"deployment.environment"`, `string_value:"dev"`, "token=[redacted]")
		wantAbsent(t, signal, text, "CANARY", "deployment.token", "telemetry.sdk")
	}
}

func TestBaggageOptions(t *testing.T) {
	serve := func() string {
		conn, received := grpcPair(t)
		client := healthpb.NewHealthClient(conn)
		web := httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, _ = client.Check(r.Context(), &healthpb.HealthCheckRequest{})
		}), "web"))
		defer web.Close()
		req, err := http.NewRequest(http.MethodGet, web.URL, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
		req.Header.Set("tracestate", "vendor=CANARY-TRACE-STATE")
		req.Header.Set("baggage", "user=CANARY-BAGGAGE")
		drain(t, req)
		return received()
	}

	got := capture(t, []Option{WithBaggage(false)}, serve)
	wantAbsent(t, "downstream", got.downstream, "CANARY-BAGGAGE")
	wantContains(t, "downstream", got.downstream, "CANARY-TRACE-STATE", "aaaaaaaaaaaaaaaa")

	got = capture(t, []Option{WithBaggage(true)}, serve)
	wantContains(t, "downstream", got.downstream, "CANARY-BAGGAGE")

	got = capture(t, []Option{WithTraceAllowAttributes()}, serve)
	wantContains(t, "downstream", got.downstream, "CANARY-BAGGAGE", "aaaaaaaaaaaaaaaa")
	wantAbsent(t, "downstream", got.downstream, "CANARY-TRACE-STATE")
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestRedactedErrorsPrintOnlyTheCode(t *testing.T) {
	buf := captureLog(t)
	capture(t, []Option{WithRedactedErrors(true)}, func() string {
		otel.Handle(fmt.Errorf("traces export: %w", status.Error(grpccodes.Unavailable, "password=CANARY-TEXT")))
		for range 100 {
			otel.Handle(errors.New("CANARY-BURST"))
		}
		return ""
	})
	wantAbsent(t, "log", buf.String(), "CANARY")
	wantContains(t, "log", buf.String(), "Unavailable")
	if lines := strings.Count(buf.String(), "\n"); lines != 1 {
		t.Errorf("got %d lines, want 1: %q", lines, buf.String())
	}
}

// Malformed settings print at exporter creation, from the SDK's own logger and
// default handler, which write to the real stderr: only another process shows it.
func TestMalformedSettingsStayOffStderr(t *testing.T) {
	env := []string{
		"OTEL_EXPORTER_OTLP_HEADERS=Authorization Bearer CANARY-HEADER,x-key=CANARY%ZZESCAPE",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS=Bearer CANARY-TRACES-HEADER",
		"OTEL_RESOURCE_ATTRIBUTES=CANARY-RESOURCE,a=b",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://user:CANARY-PASSWORD@[bad",
	}
	raw := child(t, "init", env...)
	wantContains(t, "stderr", raw, "CANARY-HEADER", "CANARY-RESOURCE", "CANARY-PASSWORD", "CANARY-HANDLED")

	redacted := child(t, "init-redacted", env...)
	wantAbsent(t, "stderr", redacted, "CANARY")
	wantContains(t, "stderr", redacted, "go-otel", "init done")

	// On its own, so the handler's line doesn't use up the rate limit.
	redacted = child(t, "init-redacted", "OTEL_EXPORTER_OTLP_HEADERS=Authorization Bearer CANARY-HEADER")
	wantAbsent(t, "stderr", redacted, "CANARY")
	wantContains(t, "stderr", redacted, "telemetry error: parse headers")
}

// child runs this test binary again as a service would run, returning its
// combined output.
func child(t *testing.T, mode string, env ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildProcess$")
	cmd.Env = append(append(os.Environ(), "GO_OTEL_CHILD="+mode), env...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestChildProcess(t *testing.T) {
	ctx := context.Background()
	switch os.Getenv("GO_OTEL_CHILD") {
	case "init", "init-redacted":
		opts := []Option{}
		if os.Getenv("GO_OTEL_CHILD") == "init-redacted" {
			opts = append(opts, WithRedactedErrors(true))
		}
		shutdown, err := InitWithOptions(ctx, "child", "127.0.0.1:1", opts...)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("init done")
		otel.Handle(errors.New("CANARY-HANDLED"))
		short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_ = shutdown(short)
	case "fatal":
		_, err := InitWithOptions(ctx, "child", os.Getenv("GO_OTEL_COLLECTOR"), WithLogs(true))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = LogWriter().Write([]byte(jsonLine(os.Getenv("GO_OTEL_LEVEL"), "dying", "")))
		os.Exit(1)
	}
}

// zerolog's Fatal and Panic end the process right after writing, so a queued
// record would never leave.
func TestFatalLineLeavesBeforeTheProcessEnds(t *testing.T) {
	for _, level := range []string{"fatal", "panic"} {
		t.Run(level, func(t *testing.T) {
			c := &otlpCollector{}
			c.serve(t, "127.0.0.1:0", nil)
			out := child(t, "fatal", "GO_OTEL_COLLECTOR="+c.addr, "GO_OTEL_LEVEL="+level)
			records, _ := c.exported()
			if len(records) != 1 {
				t.Fatalf("got %d records, want 1; child said: %s", len(records), out)
			}
			if got := records[0].GetSeverityText(); got != level {
				t.Fatalf("severity %q, want %q", got, level)
			}
		})
	}
}

func TestLogRecheck(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"fatal line is filtered like any other", jsonLine("fatal", "password=CANARY-FATAL", `"error":"CANARY-ERROR","stack":"CANARY-STACK"`), 1},
		{"panic line is filtered like any other", jsonLine("panic", "token=CANARY-PANIC", `"panic":"CANARY-VALUE"`), 1},
		{"console format is not exported", "12:00:00 INF CANARY-CONSOLE user=CANARY-USER\n", 0},
		{"console format with color is not exported", "\x1b[32mINF\x1b[0m CANARY-COLOR\n", 0},
		{"a trace id that isn't hex stops at the parser", jsonLine("info", "ok", `"trace_id":"CANARY-TRACE","span_id":"CANARY-SPAN"`), 1},
		{"a time that isn't one stops at the parser", strings.Replace(jsonLine("info", "ok", ""), logTime, "CANARY-TIME", 1), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records, text := collectLogs(t, []Option{WithLogs(true), WithLogMinLevel(LogLevelTrace)}, tc.line)
			if len(records) != tc.want {
				t.Fatalf("got %d records, want %d", len(records), tc.want)
			}
			wantAbsent(t, "logs", text, "CANARY")
		})
	}
}

// The options win over the environment in the exporters, but that is the SDK's
// behaviour and not this package's, so it's pinned here.
func TestEnvironmentCannotRedirectExports(t *testing.T) {
	other := &otlpCollector{}
	other.serve(t, "127.0.0.1:0", nil)
	for _, name := range []string{"ENDPOINT", "TRACES_ENDPOINT", "METRICS_ENDPOINT", "LOGS_ENDPOINT"} {
		t.Setenv("OTEL_EXPORTER_OTLP_"+name, "http://"+other.addr)
	}
	got := capture(t, []Option{WithLogs(true)}, func() string {
		_, span := otel.Tracer("filter").Start(context.Background(), "a")
		span.End()
		NewCounter("filter.count", "Count.").Add(context.Background(), 1)
		write(t, jsonLine("info", "ok", ""))
		return ""
	})
	wantContains(t, "wire", got.String(), "filter.count", `name:"a"`, `string_value:"ok"`)
	records, _ := other.exported()
	if other.spans.Load() != 0 || other.metrics.Load() != 0 || len(records) != 0 {
		t.Fatalf("the other collector got spans=%d metrics=%d logs=%d", other.spans.Load(), other.metrics.Load(), len(records))
	}
}

func TestExportStateErrorsGoThroughTheRedactor(t *testing.T) {
	resetTelemetry(t)
	c := &otlpCollector{reject: func(context.Context) error {
		return status.Error(grpccodes.PermissionDenied, "token=CANARY-ECHO eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln")
	}}
	c.serve(t, "127.0.0.1:0", nil)
	shutdown, err := Init(context.Background(), "test-service", c.addr)
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	_, span := otel.Tracer("filter").Start(context.Background(), "a")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdown(ctx)
	got := ExportState().Traces.LastError
	if got == "" {
		t.Fatal("no error recorded")
	}
	wantAbsent(t, "LastError", got, "CANARY", "eyJ")
}

func TestTelemetryFilterOffLeavesEverythingOff(t *testing.T) {
	setResourceCanaries(t)
	got := capture(t, []Option{WithTelemetryFilter(true), WithTelemetryFilter(false)}, func() string { return emitEverywhere(t) })
	wantContains(t, "traces", got.traces, "CANARY-SPAN-NAME", "CANARY-RESOURCE")
	wantContains(t, "metrics", got.metrics, "CANARY-METRIC-ATTR", "exemplars:")
	wantContains(t, "downstream", got.downstream, "CANARY-BAGGAGE")
}

// The SDK limits each instrument to 2000 series unless this variable says
// otherwise, and a value from outside can fill them.
func TestMetricFilterHoldsTheCardinalityLimit(t *testing.T) {
	t.Setenv("OTEL_GO_X_CARDINALITY_LIMIT", "-1")
	got := capture(t, []Option{WithMetricAllowAttributes("value")}, func() string {
		counter := NewCounter("filter.count", "Count.")
		for i := range metricCardinalityLimit + 10 {
			counter.Add(context.Background(), 1, KV("value", i))
		}
		return ""
	})
	wantContains(t, "metrics", got.metrics, "otel.metric.overflow")
}
