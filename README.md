# go-otel

> Tiny OpenTelemetry bootstrap for Go services

## Install

```bash
go get github.com/Bugs5382/go-otel
```

## Develop

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

## License

MIT (c) 2026 Shane
