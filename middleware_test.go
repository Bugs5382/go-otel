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
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capableWriter is an http.ResponseWriter that also supports the optional
// Hijacker, Flusher, and Pusher interfaces, recording when each is exercised.
// It models a real server writer (issue #8) so we can assert the Metrics
// wrapper stays transparent to WebSocket upgrades, SSE flushing, and HTTP/2
// push.
type capableWriter struct {
	http.ResponseWriter
	hijacked bool
	flushed  bool
	pushed   string
}

func (c *capableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c.hijacked = true
	return nil, nil, nil
}

func (c *capableWriter) Flush() { c.flushed = true }

func (c *capableWriter) Push(target string, _ *http.PushOptions) error {
	c.pushed = target
	return nil
}

// Issue #8: the Metrics wrapper must expose http.Hijacker and delegate to the
// underlying writer so WebSocket upgrades succeed on instrumented routes.
func TestMetricsPreservesHijacker(t *testing.T) {
	t.Parallel()

	var sawHijacker, hijacked bool
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		sawHijacker = ok
		if ok {
			if _, _, err := hj.Hijack(); err == nil {
				hijacked = true
			}
		}
	}))

	writer := &capableWriter{ResponseWriter: httptest.NewRecorder()}
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/ws", nil))

	if !sawHijacker {
		t.Fatal("Metrics wrapper does not expose http.Hijacker")
	}
	if !hijacked || !writer.hijacked {
		t.Fatal("Hijack did not delegate to the underlying ResponseWriter")
	}
}

// Issue #8: the wrapper must forward http.Flusher for SSE / streaming.
func TestMetricsPreservesFlusher(t *testing.T) {
	t.Parallel()

	var sawFlusher bool
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			f.Flush()
		}
	}))

	writer := &capableWriter{ResponseWriter: httptest.NewRecorder()}
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/sse", nil))

	if !sawFlusher {
		t.Fatal("Metrics wrapper does not expose http.Flusher")
	}
	if !writer.flushed {
		t.Fatal("Flush did not delegate to the underlying ResponseWriter")
	}
}

// Issue #8: the wrapper must forward http.Pusher for HTTP/2 server push.
func TestMetricsPreservesPusher(t *testing.T) {
	t.Parallel()

	var sawPusher bool
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		p, ok := w.(http.Pusher)
		sawPusher = ok
		if ok {
			_ = p.Push("/style.css", nil)
		}
	}))

	writer := &capableWriter{ResponseWriter: httptest.NewRecorder()}
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !sawPusher {
		t.Fatal("Metrics wrapper does not expose http.Pusher")
	}
	if writer.pushed != "/style.css" {
		t.Fatalf("Push did not delegate to the underlying ResponseWriter: pushed %q", writer.pushed)
	}
}

// When the underlying writer does not support an optional interface, the
// delegating method reports it rather than panicking. httptest.NewRecorder is
// not an http.Hijacker, so Hijack must surface http.ErrNotSupported.
func TestMetricsHijackUnsupportedReturnsError(t *testing.T) {
	t.Parallel()

	var hijackErr error
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hj, ok := w.(http.Hijacker); ok {
			_, _, hijackErr = hj.Hijack()
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if hijackErr == nil {
		t.Fatal("Hijack on a non-hijackable writer should return an error")
	}
}
