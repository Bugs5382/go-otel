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
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
)

// startCollector runs a plaintext gRPC server that records the method of every
// call, standing in for an OTLP collector.
func startCollector(t *testing.T) (addr string, methods <-chan string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	seen := make(chan string, 16)
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		seen <- method
		return nil
	}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), seen
}

func TestInitExportsToEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		format func(addr string) string
	}{
		{"host:port", func(addr string) string { return addr }},
		{"http url", func(addr string) string { return "http://" + addr }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, methods := startCollector(t)
			shutdown, err := Init(context.Background(), "test-service", tt.format(addr))
			if err != nil {
				t.Fatalf("Init returned error: %v", err)
			}

			_, span := otel.Tracer("test").Start(context.Background(), "op")
			span.End()
			NewCounter("test.endpoint.count", "counted").Add(context.Background(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(ctx)

			got := map[string]bool{}
			for len(got) < 2 {
				select {
				case m := <-methods:
					got[m] = true
				case <-ctx.Done():
					t.Fatalf("collector saw %v, want traces and metrics", got)
				}
			}
			for m := range got {
				if !strings.Contains(m, "TraceService") && !strings.Contains(m, "MetricsService") {
					t.Fatalf("unexpected method %q", m)
				}
			}
		})
	}
}

// An https URL must speak TLS: the first byte a server reads is the TLS
// handshake record type.
func TestInitHTTPSEndpointUsesTLS(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = lis.Close() }()
	first := make(chan byte, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err == nil {
			first <- buf[0]
		}
	}()

	shutdown, err := Init(context.Background(), "test-service", "https://"+lis.Addr().String())
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = shutdown(ctx) }()

	const tlsHandshakeRecord = 0x16
	select {
	case b := <-first:
		if b != tlsHandshakeRecord {
			t.Fatalf("first byte = %#x, want a TLS handshake", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exporter never connected")
	}
}

func TestInitRejectsBadEndpointURL(t *testing.T) {
	for _, endpoint := range []string{"ftp://host:4317", "grpc://host:4317", "http://", "http://%zz:4317"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := Init(context.Background(), "test-service", endpoint); err == nil {
				t.Fatalf("Init(%q) returned no error", endpoint)
			}
		})
	}
}
