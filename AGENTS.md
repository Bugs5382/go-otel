# AGENTS.md - go-otel

Guide for AI agents working in this repository. Pair with `CLAUDE.md` (the working agreement and
hook-enforced rules). Keep this file current when the build, layout, or public API changes.

## What this is

A tiny OpenTelemetry bootstrap library for Go services. A single `Init` wires
OTLP/gRPC trace and metric exporters into the global Tracer and Meter providers
over one shared resource, with opt-in filtered log export, and the package adds instrument helpers plus RED HTTP
middleware so consumers don't hand-roll setup.

## Using go-otel

The public surface is small and additive; keep it stable:

- `Init(ctx, service, otlpEndpoint) (shutdown func(context.Context) error, err error)` — sets up
  traces and metrics on the same endpoint and installs W3C propagation. The endpoint is a bare
  `host:port` (insecure) or an `http`/`https` URL (plaintext or TLS, by scheme); any other URL
  fails `Init`. The `shutdown` flushes both pipelines. An empty (or all-whitespace) endpoint installs the providers and propagator with
  no exporter, so trace context still propagates while spans and metrics are dropped. Existing
  trace-only callers must keep working unchanged.
- `InitWithOptions(ctx, service, otlpEndpoint, opts ...Option)` — `Init` with options; `Init` calls it
  with none. `WithInsecureSkipVerify(bool)` turns off certificate validation on `https` endpoints and
  does nothing on plaintext ones, except that logs stay off whenever it's on.
- `WithLogs(bool)` and `LogWriter() io.Writer` — opt-in log export over the same endpoint, TLS,
  resource and shutdown flush. `LogWriter` takes JSON lines, is a no-op outside a logs Init, always
  reports success and never blocks. Filters are export-only and default-deny: `WithLogAllowFields`,
  `WithLogClassOnlyFields`, `WithLogDropFields`, `WithLogMinLevel` (`LogLevel`, default info),
  `WithLogRedactPatterns`, `WithLogRedactValues`. No filter may be bypassed: don't install the global
  `LoggerProvider`, and don't let an `otel/log` type into the public API (it isn't stable at this
  version).
- `ExportState() ExportStatus` — `Configured`, plus a `SignalStatus` (`LastSuccess`, `LastErrorAt`,
  `LastError`, `Disabled`) each for `Traces`, `Metrics` and `Logs`. Follows the latest successful `Init`; zero with no
  exporters. `LastError` is redacted text, never the raw error.
- `Counter(name, description string) metric.Int64Counter` and
  `Histogram(name, description, unit string) metric.Float64Histogram` — build instruments off the
  global meter; they panic only on a malformed instrument name (a programming error). Raw-returning;
  kept unchanged for existing callers.
- `Metrics(next http.Handler) http.Handler` — RED middleware recording
  `http.server.request.duration` and `http.server.request.count` with HTTP semconv attributes.
  Depends on stdlib `net/http` only.
- Neutral surface, for callers that must never import a raw `go.opentelemetry.io/otel/...` package:
  - `Attr{Key string; Val any}` and `KV(key string, val any) Attr`.
  - `NewCounter(name, description string) CounterMetric` and
    `NewHistogram(name, description, unit string) HistogramMetric` — wrap the same instruments
    `Counter`/`Histogram` build. Named `*Metric`, not `Counter`/`Histogram`, because those names are
    already the raw-returning functions above (a func and a type can't share a name in one package).
  - `GRPCServerStatsHandler() stats.Handler` / `GRPCClientStatsHandler() stats.Handler` — wrap
    `otelgrpc.NewServerHandler`/`NewClientHandler`; the return type is a `google.golang.org/grpc/stats`
    type (gRPC transport, not otel), so a caller wires gRPC instrumentation without an otelgrpc import.

## Layout

- `otel.go` - `Init`/`InitWithOptions`: trace + metric providers (exporting, or local when no
  endpoint), resource, propagation, joined shutdown, options.
- `export.go` - `ExportState`, the exporter wrappers that feed it, and error redaction.
- `logs.go` - `LogWriter`, the log options and the field and level filters.
- `redact.go` - the redactor for exported log text.
- `metrics.go` - `Counter`/`Histogram` raw instrument helpers, plus the neutral `CounterMetric`/
  `HistogramMetric` interfaces and `NewCounter`/`NewHistogram` constructors that wrap them.
- `attr.go` - neutral `Attr`/`KV`, and the internal conversion to `attribute.KeyValue`.
- `grpc.go` - `GRPCServerStatsHandler`/`GRPCClientStatsHandler`, wrapping `otelgrpc`.
- `middleware.go` - `Metrics` RED HTTP middleware.
- `doc.go` - package doc.
- `*_test.go` - table-free tests; metrics tests assert via an SDK `ManualReader`, not a collector.
  `export_test.go` runs a real in-process OTLP/gRPC collector, with TLS where it matters, and
  `logs_test.go` uses it for every log filter.
- `example_test.go` - runnable `Example`/`ExampleGRPCServerStatsHandler` for the neutral surface.

## Build, test, lint

- Build: `task build`
- Test: `task test` (no external service required; tests use an in-process manual reader and collector)
- Lint: `task lint` (runs tests, then gofmt check + golangci-lint + yamllint)
- License headers: `task license` (verify) / `task license:fix` (inject)

## Conventions and gotchas

- See `CLAUDE.md` for the branch/commit/PR rules; they are enforced by the git hooks in
  `.claude/hooks` (run `bash .claude/hooks/install.sh` once per clone).
- Open every PR as a draft. CI skips drafts, so run the full checks locally, push once they pass,
  and mark the PR ready when the work is finished; see CLAUDE.md "CI and Actions minutes".
- Keep `Init`'s signature stable — trace-only consumers depend on it. Add capabilities additively.
- Build the resource without a pinned semconv schema URL: the service attributes are schemaless and
  merged over `resource.Default()`, so they inherit the SDK's schema URL. Importing a versioned
  `semconv` package for the resource makes `resource.Merge` fail with `ErrSchemaURLConflict` the
  next time the SDK moves to a newer semconv.
- Traces and metrics use separate OTLP exporters (the SDK has no single dual-signal exporter) but
  share one endpoint and one resource, so keep them constructed together in `Init`.
