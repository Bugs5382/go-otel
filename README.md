# go-otel 🔭

> Tiny OpenTelemetry bootstrap for Go services — one call wires OTLP/gRPC trace **and** metric exporters into global providers, installs W3C trace-context propagation, and ships instrument helpers plus RED HTTP middleware.

## 📦 Install

```bash
go get github.com/Bugs5382/go-otel
```

## 🚀 Usage

`Init` sets up both traces and metrics on the same OTLP/gRPC endpoint, sharing
one resource tagged with `service.name`. The returned `shutdown` flushes both
pipelines.

```go
shutdown, err := otel.Init(ctx, "my-service", "localhost:4317")
if err != nil {
	log.Fatal(err)
}
defer shutdown(context.Background())
```

Traces export over OTLP/gRPC (insecure) to the given endpoint. A logs exporter
is planned for a later release. 📈

## 📊 Metrics

After `Init`, create instruments off the global meter with the helpers and
record against them:

```go
requests := otel.Counter("orders.placed", "Orders placed.")
requests.Add(ctx, 1)

latency := otel.Histogram("db.query.duration", "Query duration.", "s")
latency.Record(ctx, 0.042)
```

Wrap an `http.Handler` with the middleware to record the RED signals (request
rate, errors, duration) using HTTP semantic-convention names — the templated
route is picked up automatically from a `net/http` `ServeMux`:

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /items/{id}", itemsHandler)

http.ListenAndServe(":8080", otel.Metrics(mux))
```

## 🧼 Neutral surface (no raw otel imports)

`Counter`/`Histogram` above return the raw `go.opentelemetry.io/otel/metric`
types, and stay that way for existing callers. If you'd rather your own code
never imports a `go.opentelemetry.io/otel/...` package, use the neutral
constructors instead — they wrap the same instruments behind interfaces built
on a neutral attribute type:

```go
orders := otel.NewCounter("orders.placed", "Orders placed.")
orders.Add(ctx, 1, otel.KV("outcome", "ok"), otel.KV("region", "us-east"))

latency := otel.NewHistogram("db.query.duration", "Query duration.", "s")
latency.Record(ctx, 0.042, otel.KV("outcome", "ok"))
```

`NewCounter`/`NewHistogram` return `CounterMetric`/`HistogramMetric` — not
`Counter`/`Histogram` — because those names are already taken by the
raw-returning functions above (a function and a type can't share a name in one
Go package). `KV(key string, val any) Attr` accepts `string`, `bool`, `int`,
`int64`, `float64`, and slices of those directly; any other value type is
rendered with `fmt.Sprintf`.

## 🔌 gRPC server instrumentation

`GRPCServerStatsHandler`/`GRPCClientStatsHandler` wrap `otelgrpc` internally
and hand back a plain `google.golang.org/grpc/stats.Handler`, so wiring gRPC
tracing/metrics never requires importing `otelgrpc` (or any other raw otel
package) yourself:

```go
s := grpc.NewServer(grpc.StatsHandler(otel.GRPCServerStatsHandler()))

conn, err := grpc.NewClient(target, grpc.WithStatsHandler(otel.GRPCClientStatsHandler()))
```

## 🛠 Develop

```bash
task build    # go build ./...
task test     # go test ./...
task lint     # gofmt check + golangci-lint + yamllint
task license  # inject MIT headers (golic)
```

## ⚖️ License

MIT © 2026 Shane
