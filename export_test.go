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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Init is relied on by trace-only callers, so its signature must not change.
var _ func(context.Context, string, string) (func(context.Context) error, error) = Init

// otlpCollector is an in-process OTLP/gRPC collector. It counts what it
// receives, and answers every export with reject's error when reject is set.
type otlpCollector struct {
	addr    string
	spans   atomic.Int64
	metrics atomic.Int64
	reject  func(ctx context.Context) error
	srv     *grpc.Server
}

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	c *otlpCollector
}

func (s traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if s.c.reject != nil {
		return nil, s.c.reject(ctx)
	}
	s.c.spans.Add(int64(len(req.GetResourceSpans())))
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type metricService struct {
	colmetricpb.UnimplementedMetricsServiceServer
	c *otlpCollector
}

func (s metricService) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	if s.c.reject != nil {
		return nil, s.c.reject(ctx)
	}
	s.c.metrics.Add(int64(len(req.GetResourceMetrics())))
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

// serve starts c on addr, with TLS when cert is non-nil.
func (c *otlpCollector) serve(t *testing.T, addr string, cert *tls.Certificate) {
	t.Helper()
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen on %s: %v", addr, err)
	}
	var opts []grpc.ServerOption
	if cert != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{*cert},
			MinVersion:   tls.VersionTLS12,
		})))
	}
	srv := grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(srv, traceService{c: c})
	colmetricpb.RegisterMetricsServiceServer(srv, metricService{c: c})
	go func() { _ = srv.Serve(lis) }()
	c.addr, c.srv = lis.Addr().String(), srv
	t.Cleanup(srv.Stop)
}

// selfSignedCert returns a certificate for 127.0.0.1 that no pool trusts.
func selfSignedCert(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "collector.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// emit records a span and a metric, then flushes both pipelines once. The
// periodic exporters would otherwise wait for their own schedule.
func emit(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, span := otel.Tracer("test").Start(ctx, "op")
	span.End()
	NewCounter("test.export.count", "counted").Add(ctx, 1)
	_ = otel.GetTracerProvider().(*sdktrace.TracerProvider).ForceFlush(ctx)
	_ = otel.GetMeterProvider().(*sdkmetric.MeterProvider).ForceFlush(ctx)
}

func initForTest(t *testing.T, endpoint string, opts ...Option) {
	t.Helper()
	// Keeps an export to a dead collector from retrying for the SDK's default
	// ten seconds.
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "500")
	shutdown, err := InitWithOptions(context.Background(), "test-service", endpoint, opts...)
	if err != nil {
		t.Fatalf("InitWithOptions returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})
}

func assertDelivered(t *testing.T, c *otlpCollector, s ExportStatus) {
	t.Helper()
	if c.spans.Load() == 0 || c.metrics.Load() == 0 {
		t.Fatalf("collector got %d span batches and %d metric batches, want both", c.spans.Load(), c.metrics.Load())
	}
	for name, sig := range map[string]SignalStatus{"traces": s.Traces, "metrics": s.Metrics} {
		if sig.LastSuccess.IsZero() {
			t.Errorf("%s: no success recorded: %+v", name, sig)
		}
		if sig.LastError != "" {
			t.Errorf("%s: unexpected error: %s", name, sig.LastError)
		}
	}
}

func assertFailed(t *testing.T, s ExportStatus) {
	t.Helper()
	for name, sig := range map[string]SignalStatus{"traces": s.Traces, "metrics": s.Metrics} {
		if sig.LastError == "" || sig.LastErrorAt.IsZero() {
			t.Errorf("%s: no error recorded: %+v", name, sig)
		}
		if !sig.LastSuccess.IsZero() {
			t.Errorf("%s: unexpected success: %+v", name, sig)
		}
	}
}

func TestExportStateNotConfigured(t *testing.T) {
	initForTest(t, "   ")
	emit(t)
	if s := ExportState(); s != (ExportStatus{}) {
		t.Fatalf("ExportState() = %+v, want the zero value", s)
	}
}

func TestExportStateResetsOnNextInit(t *testing.T) {
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, c.addr)
	emit(t)
	if !ExportState().Configured {
		t.Fatal("Configured = false with an endpoint")
	}
	initForTest(t, "")
	if s := ExportState(); s != (ExportStatus{}) {
		t.Fatalf("ExportState() after an empty Init = %+v, want the zero value", s)
	}
}

func TestSelfSignedCollector(t *testing.T) {
	tests := []struct {
		name    string
		opts    []Option
		deliver bool
	}{
		{"verify by default", nil, false},
		{"verify when asked", []Option{WithInsecureSkipVerify(false)}, false},
		{"skip", []Option{WithInsecureSkipVerify(true)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &otlpCollector{}
			c.serve(t, "127.0.0.1:0", selfSignedCert(t))
			initForTest(t, "https://"+c.addr, tt.opts...)
			emit(t)

			s := ExportState()
			if !s.Configured {
				t.Fatal("Configured = false with an endpoint")
			}
			if tt.deliver {
				assertDelivered(t, c, s)
				return
			}
			assertFailed(t, s)
			if !strings.Contains(s.Traces.LastError, "certificate") {
				t.Errorf("traces error %q does not name the certificate", s.Traces.LastError)
			}
			if c.spans.Load() != 0 || c.metrics.Load() != 0 {
				t.Fatalf("collector got data over an unverified connection")
			}
		})
	}
}

// A plaintext endpoint must stay plaintext: TLS credentials here would fail
// against a server that does not speak TLS.
func TestInsecureSkipVerifyLeavesPlaintextAlone(t *testing.T) {
	for _, format := range []string{"%s", "http://%s"} {
		t.Run(format, func(t *testing.T) {
			c := &otlpCollector{}
			c.serve(t, "127.0.0.1:0", nil)
			initForTest(t, strings.Replace(format, "%s", c.addr, 1), WithInsecureSkipVerify(true))
			emit(t)
			assertDelivered(t, c, ExportState())
		})
	}
}

func TestExportStateOutageAndRecovery(t *testing.T) {
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, c.addr)
	emit(t)
	before := ExportState()
	assertDelivered(t, c, before)

	c.srv.Stop()
	emit(t)
	down := ExportState()
	for name, sig := range map[string]SignalStatus{"traces": down.Traces, "metrics": down.Metrics} {
		if sig.LastError == "" || sig.LastErrorAt.IsZero() {
			t.Fatalf("%s: outage not recorded: %+v", name, sig)
		}
		if !sig.LastErrorAt.After(sig.LastSuccess) {
			t.Fatalf("%s: error at %v is not after success at %v", name, sig.LastErrorAt, sig.LastSuccess)
		}
	}
	if down.Traces.LastSuccess != before.Traces.LastSuccess {
		t.Fatal("an outage moved the last success time")
	}

	c.serve(t, c.addr, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		emit(t)
		up := ExportState()
		if up.Traces.LastSuccess.After(down.Traces.LastErrorAt) && up.Metrics.LastSuccess.After(down.Metrics.LastErrorAt) {
			if up.Traces.LastError == "" {
				t.Fatal("recovery cleared the last error, want it kept with its time")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no success recorded after recovery: %+v", up)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestExportStateErrorsCarryNoSecrets(t *testing.T) {
	const headerSecret, urlSecret = "header-s3cret value", "url-s3cret"
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=header-s3cret%20value,x-empty=")

	c := &otlpCollector{reject: func(ctx context.Context) error {
		md, _ := metadata.FromIncomingContext(ctx)
		return status.Errorf(codes.Unauthenticated, "bad token %q from https://collector:%s@192.0.2.1:4317",
			md.Get("authorization"), urlSecret)
	}}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, "http://collector:"+urlSecret+"@"+c.addr)
	emit(t)

	s := ExportState()
	assertFailed(t, s)
	for _, msg := range []string{s.Traces.LastError, s.Metrics.LastError} {
		if !strings.Contains(msg, "bad token") {
			t.Errorf("error lost its detail: %q", msg)
		}
		for _, secret := range []string{headerSecret, "header-s3cret%20value", urlSecret} {
			if strings.Contains(msg, secret) {
				t.Errorf("error %q contains %q", msg, secret)
			}
		}
	}
}

func TestInitErrorsCarryNoCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://user:url-s3cret@", "http://user:url-s3cret@%zz:4317"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := Init(context.Background(), "test-service", endpoint)
			if err == nil {
				t.Fatal("Init returned no error")
			}
			if strings.Contains(err.Error(), "url-s3cret") {
				t.Fatalf("error %q contains the password", err)
			}
		})
	}
}

func TestExportStateConcurrentUse(t *testing.T) {
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, c.addr)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = ExportState()
				}
			}
		})
	}
	for range 5 {
		emit(t)
	}
	close(done)
	wg.Wait()
	assertDelivered(t, c, ExportState())
}
