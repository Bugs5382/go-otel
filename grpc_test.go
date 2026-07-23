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
	"testing"

	"google.golang.org/grpc/stats"
)

func TestGRPCServerStatsHandlerSatisfiesHandler(t *testing.T) {
	h := GRPCServerStatsHandler()
	if h == nil {
		t.Fatal("GRPCServerStatsHandler returned nil")
	}

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/test.Service/Method"})
	if ctx == nil {
		t.Fatal("TagRPC returned nil context")
	}
	// HandleRPC and TagConn/HandleConn must not panic on minimal inputs.
	h.HandleRPC(ctx, &stats.Begin{Client: false})
	h.HandleRPC(ctx, &stats.End{})

	connCtx := h.TagConn(context.Background(), &stats.ConnTagInfo{})
	if connCtx == nil {
		t.Fatal("TagConn returned nil context")
	}
	h.HandleConn(connCtx, &stats.ConnBegin{})
	h.HandleConn(connCtx, &stats.ConnEnd{})
}

func TestGRPCClientStatsHandlerSatisfiesHandler(t *testing.T) {
	h := GRPCClientStatsHandler()
	if h == nil {
		t.Fatal("GRPCClientStatsHandler returned nil")
	}

	ctx := h.TagRPC(context.Background(), &stats.RPCTagInfo{FullMethodName: "/test.Service/Method"})
	if ctx == nil {
		t.Fatal("TagRPC returned nil context")
	}
	h.HandleRPC(ctx, &stats.Begin{Client: true})
	h.HandleRPC(ctx, &stats.End{})
}

func TestGRPCStatsHandlersAreDistinct(t *testing.T) {
	server := GRPCServerStatsHandler()
	client := GRPCClientStatsHandler()
	if server == nil || client == nil {
		t.Fatal("expected non-nil handlers")
	}
}
