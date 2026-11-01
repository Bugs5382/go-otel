# go-otel 🔭

> 📡 Tiny OpenTelemetry bootstrap for Go services — one call wires OTLP/gRPC trace **and** metric exporters into global providers, installs W3C trace-context propagation, and ships instrument helpers, RED HTTP middleware, opt-in filtered log export and default-deny telemetry filters.

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

Traces and metrics export over OTLP/gRPC to the given endpoint, unfiltered unless
you ask for the [telemetry filter](#-telemetry-filter). Logs are opt-in: see
[Logs](#-logs). 📈

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
them back, and it goes through the [redactor](#redaction). A header set some other
way isn't known to go-otel, and neither is a secret a collector makes up on its
own, so treat `LastError` as text for operators.

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

`Write` always reports success and doesn't wait on the network, with one
exception: a `fatal` or `panic` line waits up to 2 seconds for the queue to flush,
because the process ends right after one. If the collector falls behind, the queue
drops its oldest records, and your logs still reach stdout. Only JSON lines are
read, so `LOG_FORMAT=console` exports nothing.

### What leaves the process

🔒 **Default-deny.** A record carries its time, level, `service` and `message`,
plus `trace_id` and `span_id` as the record's trace correlation, which only pass
as valid hex ids. Everything else is dropped unless you name it. These filters apply to the export only: whatever
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

⚠️ With `WithInsecureSkipVerify(true)`, on any endpoint, logs stay off even with
`WithLogs(true)`. Traces and metrics carry on, and `ExportState().Logs.Disabled`
says why. To export logs to a private certificate, trust it instead, for example
with `OTEL_EXPORTER_OTLP_CERTIFICATE`. A plaintext endpoint (`host:port` or
`http://`) doesn't authenticate the collector either, and logs do export over one,
so use `https` when the network between the service and the collector isn't yours.

## 🛡️ Telemetry filter

With no options, go-otel exports what the SDK and its instrumentation give it, and
that is a lot for a service that holds secrets. In a test run with a canary on
every path, these all reached the collector:

- exception events with stack traces, and status text: `otelgrpc` copies the gRPC
  status message there, and `otelpgx` copies the database error;
- span names holding full SQL, or whatever gRPC method a client sent;
- `url.path`, `user_agent.original`, `client.address` from `X-Forwarded-For` and
  `server.address` from `Host`, and `url.full` with its query on the `otelhttp`
  transport;
- a caller's `tracestate`, on the span and forwarded downstream, and a caller's
  `baggage`, forwarded on every call;
- everything in `OTEL_RESOURCE_ATTRIBUTES`, on all three signals;
- raw paths and arbitrary methods in the `Metrics` labels, and exemplars.

On stderr, the SDK printed malformed `OTEL_EXPORTER_OTLP_HEADERS` entries and
endpoint URLs with their passwords.

`WithTelemetryFilter(true)` turns on every filter, default-deny, and the allow
options name what may leave:

```go
shutdown, err := otel.InitWithOptions(ctx, "my-service", "https://collector:4317",
	otel.WithTelemetryFilter(true),
	otel.WithTraceAllowSpanNames(regexp.MustCompile(`^my\.package\.v1\.[A-Za-z]+/[A-Za-z]+$`)),
	otel.WithTraceAllowAttributes("rpc.method", "rpc.response.status_code"),
	otel.WithMetricAllowAttributes("http.request.method", "http.route", "http.response.status_code",
		"rpc.method", "rpc.response.status_code"),
	otel.WithAttributeValuePattern("rpc.method", regexp.MustCompile(`^my\.package\.v1\.[A-Za-z]+/[A-Za-z]+$`)),
)
```

Each allow option also turns on its own filter, if you want only that one.
Without any of these options nothing changes.

| Option | Effect |
| --- | --- |
| `WithTelemetryFilter(enabled)` | All of the below, and `WithBaggage(false)` and `WithRedactedErrors(true)`. |
| `WithTraceAllowAttributes(names...)` | The attributes a span, event or link may carry. |
| `WithTraceAllowEvents(names...)` | The span events that may leave. `exception` never does, even when named. |
| `WithTraceAllowSpanNames(patterns...)` | The span names that may leave. Any other span stays in the trace, named `[span]`. |
| `WithMetricAllowAttributes(names...)` | The attributes a data point may carry. The points aggregate without the rest. |
| `WithResourceAllowAttributes(names...)` | The resource attributes that may leave, on top of `service.name`. |
| `WithAttributeValuePattern(name, pattern)` | An allowed attribute whose value doesn't match is dropped, on traces and metrics. Use it for values from outside, such as `rpc.method`. |
| `WithBaggage(enabled)` | Whether the propagator carries baggage. With it off, baggage neither comes in on a request nor goes out on a call. |
| `WithRedactedErrors(enabled)` | Replaces the SDK's error handler and internal logger. See below. |

### What leaves with the filters on

🔒 **Traces.** A span keeps its ids, times, kind and status code. The status text
goes, exception events and their stacks go, and trace state and scope attributes
go, on the span and its links. Allowed attributes go through the
[redactor](#redaction) and `WithLogRedactPatterns` and `WithLogRedactValues` apply.
One that holds a private key with no END line, is over 64 KiB, or is bytes, a map or
a nested value is dropped. The propagator also stops taking `tracestate` from
callers, so it isn't forwarded.

🔒 **Metrics.** An allowed attribute whose value the redactor would change is
dropped rather than rewritten, so redaction never merges or renames a series. That
check costs about 12 microseconds per string attribute per measurement. Exemplars
stay off whatever `OTEL_METRICS_EXEMPLAR_FILTER` says, because an exemplar carries
the attributes a view drops. Scope attributes go, and each instrument stays at 2000
series whatever `OTEL_GO_X_CARDINALITY_LIMIT` says. The `Metrics` middleware reports
`unmatched` for a request no route matched and `_OTHER` for a non-standard method,
where it would otherwise report what the client sent.

🔒 **Resource.** Only `service.name` and what you name leave, on traces, metrics and
logs. The filter runs on each OTLP request as it's sent, because each SDK provider
merges `OTEL_RESOURCE_ATTRIBUTES` back into the resource it's given.

🔒 **Errors.** The SDK's error handler becomes one that logs the gRPC code, and its
internal logger one that logs only the SDK's own message, through the standard
`log` package, at most one line every 30 seconds. No error text is printed, because
a malformed setting has nothing around it to say it's secret. That also means a bad
setting shows only as `telemetry error: parse headers`: turn the option off in a
safe environment to see more. `ExportState` still has the redacted text of the last
export error. The option replaces a handler set before `Init`, and both stay
installed after `shutdown`.

### What the filters can't do

⚠️ These stay your call, because go-otel can't see them or can't change them:

- A secret in an allowed attribute with no shape around it. The allow-list is the
  main defense, as it is for logs: keep it short, and don't allow `url.path`,
  `db.query.text`, `user_agent.original` or `client.address` without a reason.
- `traceparent` from outside. A public endpoint should start its own trace, with
  `otelhttp.WithPublicEndpoint()`, so a caller can't choose your trace ids or
  sampling.
- What instrumentation puts in a name you allow. `otelpgx` builds its span name
  from the SQL: `otelpgx.WithTrimSQLInSpanName()` gives names a pattern can
  allow, and `otelpgx.WithDisableSQLStatementInAttributes()` stops the text at the
  source. Don't use `otelpgx.WithIncludeQueryParameters()`.
- OTLP headers from `OTEL_EXPORTER_OTLP_HEADERS` go to whatever collector the
  endpoint names, in the clear on a plaintext endpoint.

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

The pattern is read off the request after the mux runs, so a handler between the
middleware and the mux that copies the request (`r.WithContext`) loses it. A request
with no pattern reports its raw path as `http.route`, or `unmatched` with the
[telemetry filter](#-telemetry-filter) on.

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
