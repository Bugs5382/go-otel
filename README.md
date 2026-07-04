# go-otel

Tiny OpenTelemetry bootstrap for Go services: one call wires an OTLP/gRPC trace
exporter into a global `TracerProvider` and installs W3C trace-context
propagation.

## Install

```bash
go get github.com/Bugs5382/go-otel
```

## Usage

```go
shutdown, err := otel.Init(ctx, "my-service", "localhost:4317")
if err != nil {
	log.Fatal(err)
}
defer shutdown(context.Background())
```

Traces are exported over OTLP/gRPC (insecure) to the given endpoint, tagged with
`service.name`. Metrics and logs exporters are planned for later releases.

## License

MIT © Bugs5382
