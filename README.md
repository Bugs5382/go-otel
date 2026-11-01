# go-otel 🔭

> 📡 Tiny OpenTelemetry bootstrap for Go services — one call wires OTLP/gRPC trace **and** metric exporters into global providers, installs W3C trace-context propagation, and ships instrument helpers, RED HTTP middleware and opt-in filtered log export.

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

Traces and metrics export over OTLP/gRPC to the given endpoint. Logs are opt-in:
see [Logs](#-logs). 📈

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
headers. The option has no effect on a plaintext endpoint, except that logs stay
off whenever it's on: see [Logs](#-logs).

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

`Traces`, `Metrics` and `Logs` each hold `LastSuccess`, `LastErrorAt` and
`LastError`. A zero time means that outcome hasn't happened yet, and `LastError`
stays after a later success, so compare the two times. The signals fill in on
the exporters' own schedules: about 5 seconds for spans, 60 seconds for metrics
and 1 second for logs. `Disabled` says why a signal isn't exporting, and is empty
when it is: logs show it when `WithLogs` isn't set, or when skip-verify keeps
them off.

`Configured` is false before `Init` and after an `Init` with an empty endpoint.
The state follows the latest successful `Init`, and is safe to read from any
goroutine. `LastError` has the endpoint password, any URL userinfo and the values
of the `OTEL_EXPORTER_OTLP_HEADERS` variables removed, even if a collector echoes
them back. A header set some other way isn't known to go-otel, and neither is a
secret a collector makes up on its own, so treat `LastError` as text for
operators.

## 📜 Logs

Log export is off unless you ask for it. `WithLogs(true)` adds a log pipeline on
the same endpoint, TLS settings, resource and shutdown flush as traces and
metrics. `LogWriter()` is its only way in: an `io.Writer` that takes JSON log
lines and filters them. The global `LoggerProvider` is left alone on purpose, so
nothing can reach the collector around the filters.

With [go-log](https://github.com/Bugs5382/go-log), hand it to `WithOutput`. The
logger can be built before `Init`, because the writer discards everything until
then and again after `shutdown`:

```go
logger := log.NewLoggerWithOptions("my-service",
	log.WithOutput(io.MultiWriter(os.Stdout, otel.LogWriter())))

shutdown, err := otel.InitWithOptions(ctx, "my-service", "https://collector:4317",
	otel.WithLogs(true),
	otel.WithLogAllowFields("route", "status"),
)
```

`Write` always reports success and never waits on the network. If the collector
falls behind, the queue drops its oldest records, and your logs still reach
stdout. Only JSON lines are read, so `LOG_FORMAT=console` exports nothing.

### What leaves the process

🔒 **Default-deny.** A record carries its time, level, `service` and `message`,
plus `trace_id` and `span_id` as the record's trace correlation. Everything else
is dropped unless you name it. These filters apply to the export only: whatever
else your logger writes to is untouched.

| Option | Effect |
| --- | --- |
| `WithLogAllowFields(names...)` | These fields may be exported. Every other field is dropped. |
| `WithLogClassOnlyFields(names...)` | Export the field's class (`[string]`, `[number]`, `[bool]`, `[object]`, `[array]`, `[null]`), never its text. |
| `WithLogDropFields(names...)` | Never export these. Beats both options above. |
| `WithLogMinLevel(level)` | The lowest level exported. The default is `info`, which is also what an unknown name gets. |
| `WithLogRedactPatterns(patterns...)` | Your own `*regexp.Regexp` shapes, added to the rules below. |
| `WithLogRedactValues(values...)` | Exact values to remove wherever they appear. Anything under 8 bytes is ignored. |

The class-only and drop marks narrow the allow-list and never extend it, and they
also apply to `service` and `message`. An allowed object or array exports only
its class, because the names inside it were never allowed.

A line isn't exported at all if it isn't a JSON object, has no level or an
unknown one, or is over 64 KiB.

### Redaction

The message and every allowed string go through the redactor, which removes:

- terminal escapes and control characters;
- PEM private keys, on one line or many. A key with no END line withholds the
  whole record, because where it stops can't be known;
- the whole value of `Authorization`, `Proxy-Authorization`, `Cookie`,
  `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`, `X-Vault-Token` and the Kubernetes
  `last-applied-configuration` annotation;
- URL passwords, an empty user's (`redis://:<password>@`) too;
- the value of any `key=value`, `key: value` or JSON field whose key says it's
  secret: `password`, `secret`, `token`, `api_key`, `credential`, `kek`,
  `enc_key`, `signature` and the like, in any case and with any prefix or suffix.
  A key that only names a secret (`secretName`, `token_type`) is kept;
- JWTs, GitHub tokens, age secret keys, and bearer and basic credentials.

Masked values read `[redacted]`. Numbers and booleans in allowed fields pass as
they are.

⚠️ The redactor works on shapes, so it can't recognize a secret with nothing
around it that says so. There's no high-entropy matching, because that also
blanks UUIDs and hex ids, so a random string in an allowed field or a message is
exported unless `WithLogRedactPatterns` or `WithLogRedactValues` knows it. The
allow-list is the main defense: keep it short.

### Collectors that aren't verified

⚠️ Logs never go to a collector that isn't authenticated. With
`WithInsecureSkipVerify(true)`, on any endpoint, logs stay off even with
`WithLogs(true)`. Traces and metrics carry on, and `ExportState().Logs.Disabled`
says why. To export logs to a private certificate, trust it instead, for example
with `OTEL_EXPORTER_OTLP_CERTIFICATE`.

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
