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
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc/stats"
)

// GRPCServerStatsHandler returns an otel-instrumented gRPC stats.Handler for a
// server, wired for grpc.StatsHandler:
//
//	s := grpc.NewServer(grpc.StatsHandler(otel.GRPCServerStatsHandler()))
//
// It returns google.golang.org/grpc/stats.Handler — a gRPC transport type, not
// an otel type — so wiring gRPC instrumentation never requires a caller to
// import otelgrpc, or any other raw otel package, directly.
func GRPCServerStatsHandler() stats.Handler {
	return otelgrpc.NewServerHandler()
}

// GRPCClientStatsHandler returns the client-side counterpart, for
// grpc.WithStatsHandler on an outbound connection:
//
//	conn, err := grpc.NewClient(target, grpc.WithStatsHandler(otel.GRPCClientStatsHandler()))
func GRPCClientStatsHandler() stats.Handler {
	return otelgrpc.NewClientHandler()
}
