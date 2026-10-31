# go-otel 🔭

> 📡 Tiny OpenTelemetry bootstrap for Go services — one call wires OTLP/gRPC trace **and** metric exporters into global providers, installs W3C trace-context propagation, and ships instrument helpers plus RED HTTP middleware.

## 📦 Install

```bash
go get github.com/Bugs5382/go-otel
```

Requires OpenTelemetry Go v1.45.0 or later (the `go.opentelemetry.io/otel`
v1.45.0 module set). Releases up to v1.3.1 fail in `Init` with a schema URL
conflict once a service moves to v1.45.0, so upgrade go-otel along with otel.

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

Traces export over OTLP/gRPC to the given endpoint. A logs exporter is planned
for a later release. 📈

The endpoint is a bare `host:port` or a URL with a port:

| Endpoint | Transport |
| --- | --- |
| `collector:4317` | plaintext |
| `http://collector:4317` | plaintext |
| `https://collector:4317` | TLS |

Any other URL scheme, or a URL with no host, makes `Init` return an error.

### Self-signed collectors

An `https` endpoint verifies the collector's certificate against the system
pool. For a collector with a private or self-signed certificate, turn that off
with `InitWithOptions`, which is `Init` plus options:

```go
shutdown, err := otel.InitWithOptions(ctx, "my-service", "https://collector:4317",
	otel.WithInsecureSkipVerify(true))
```

⚠️ The connection stays encrypted, but the collector is no longer authenticated:
anyone who can intercept the connection can read your telemetry and OTLP
headers. The option has no effect on a plaintext endpoint.

### Running without a collector

Pass an empty endpoint to run with no collector at all (local development,
tests, or an environment that has none). `Init` still installs the tracer and
meter providers and the W3C propagator, so spans get real trace IDs and an
incoming `traceparent` is continued and passed on to downstream calls. No
exporter is built: spans and metrics are dropped, and `shutdown` has nothing to
flush.

To make export opt-in through the environment, pass the standard variable
straight through. Use the URL form, because the OpenTelemetry SDK also reads
that variable and logs parse errors on a bare `host:port`:

```go
shutdown, err := otel.Init(ctx, "my-service", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
```

## 🩺 Export state

Span and metric exports run in the background, so a failed one never reaches
your code. `ExportState` reports how they went, for a health or status page:

```go
state := otel.ExportState()
if !state.Configured {
	return "no collector"
}
if state.Traces.LastErrorAt.After(state.Traces.LastSuccess) {
	return "traces failing: " + state.Traces.LastError
}
```

`Traces` and `Metrics` each hold `LastSuccess`, `LastErrorAt` and `LastError`. A
zero time means that outcome hasn't happened yet, and `LastError` stays after a
later success, so compare the two times. Both signals fill in on the exporters'
own schedules: about 5 seconds for spans and 60 seconds for metrics.

`Configured` is false before `Init` and after an `Init` with an empty endpoint.
The state follows the latest successful `Init`, and is safe to read from any
goroutine. `LastError` has the endpoint password, any URL userinfo and the values
of the `OTEL_EXPORTER_OTLP_HEADERS` variables removed, even if a collector echoes
them back. A header set some other way isn't known to go-otel, and neither is a
secret a collector makes up on its own, so treat `LastError` as text for
operators.

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
