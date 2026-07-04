# AGENTS.md - go-otel

Guide for AI agents working in this repository. Pair with `CLAUDE.md` (the working agreement and
hook-enforced rules). Keep this file current when the build, layout, or public API changes.

## What this is

A tiny OpenTelemetry bootstrap library for Go services. A single `Init` wires
OTLP/gRPC trace and metric exporters into the global Tracer and Meter providers
over one shared resource, and the package adds instrument helpers plus RED HTTP
middleware so consumers don't hand-roll setup.

## Using go-otel

The public surface is small and additive; keep it stable:

- `Init(ctx, service, otlpEndpoint) (shutdown func(context.Context) error, err error)` — sets up
  traces and metrics on the same endpoint and installs W3C propagation. The `shutdown` flushes
  both pipelines. Existing trace-only callers must keep working unchanged.
- `Counter(name, description string) metric.Int64Counter` and
  `Histogram(name, description, unit string) metric.Float64Histogram` — build instruments off the
  global meter; they panic only on a malformed instrument name (a programming error).
- `Metrics(next http.Handler) http.Handler` — RED middleware recording
  `http.server.request.duration` and `http.server.request.count` with HTTP semconv attributes.
  Depends on stdlib `net/http` only.

## Layout

- `otel.go` - `Init`: trace + metric providers, resource, propagation, joined shutdown.
- `metrics.go` - `Counter`/`Histogram` instrument helpers.
- `middleware.go` - `Metrics` RED HTTP middleware.
- `doc.go` - package doc.
- `*_test.go` - table-free tests; metrics tests assert via an SDK `ManualReader`, not a collector.

## Build, test, lint

- Build: `task build`
- Test: `task test` (no external service required; tests use an in-process manual reader)
- Lint: `task lint` (runs tests, then gofmt check + golangci-lint + yamllint)
- License headers: `task license` (verify) / `task license:fix` (inject)

## Conventions and gotchas

- See `CLAUDE.md` for the branch/commit/PR rules; they are enforced by the git hooks in
  `.claude/hooks` (run `bash .claude/hooks/install.sh` once per clone).
- Keep `Init`'s signature stable — trace-only consumers depend on it. Add capabilities additively.
- Traces and metrics use separate OTLP exporters (the SDK has no single dual-signal exporter) but
  share one endpoint and one resource, so keep them constructed together in `Init`.
