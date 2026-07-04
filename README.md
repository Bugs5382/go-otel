# go-otel 🔭

> Tiny OpenTelemetry bootstrap for Go services — one call wires an OTLP/gRPC trace exporter into a global `TracerProvider` and installs W3C trace-context propagation.

## 📦 Install

```bash
go get github.com/Bugs5382/go-otel
```

## 🚀 Usage

```go
shutdown, err := otel.Init(ctx, "my-service", "localhost:4317")
if err != nil {
	log.Fatal(err)
}
defer shutdown(context.Background())
```

Spans export over OTLP/gRPC (insecure) to the given endpoint, tagged with
`service.name`. Metrics and logs exporters are planned for later releases. 📈

## 🛠 Develop

```bash
task build    # go build ./...
task test     # go test ./...
task lint     # gofmt check + golangci-lint + yamllint
task license  # inject MIT headers (golic)
```

Commit discipline, AI-tell/emoji blocking, and the pre-push gofmt/vet/lint/test gate are enforced
by the governance hooks. Install them once per clone:

```bash
bash .claude/hooks/install.sh
```

## ⚖️ License

MIT © 2026 Shane
