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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// LogLevel names a minimum level for WithLogMinLevel, using the level names in
// the JSON lines themselves, so LogLevel(flagValue) works too.
type LogLevel string

// The levels, lowest first.
const (
	LogLevelTrace LogLevel = "trace"
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
	LogLevelFatal LogLevel = "fatal"
	LogLevelPanic LogLevel = "panic"
)

const (
	// maxLogLine bounds the work one line can cost the goroutine that logs it.
	maxLogLine = 64 * 1024

	// fatalFlushWait bounds the one wait in this package's write path. zerolog
	// ends the process right after a fatal or panic line, so a record that only
	// queued would never leave.
	fatalFlushWait = 2 * time.Second

	logsNotEnabled = "logs are off: WithLogs was not set"
	logsUnverified = "logs are off: the collector's certificate is not verified"
)

var logSeverities = map[string]log.Severity{
	string(LogLevelTrace): log.SeverityTrace,
	string(LogLevelDebug): log.SeverityDebug,
	string(LogLevelInfo):  log.SeverityInfo,
	string(LogLevelWarn):  log.SeverityWarn,
	string(LogLevelError): log.SeverityError,
	string(LogLevelFatal): log.SeverityFatal,
	string(LogLevelPanic): log.SeverityFatal4,
}

type logConfig struct {
	enabled        bool
	allow          []string
	classOnly      []string
	drop           []string
	minLevel       LogLevel
	redactValues   []string
	redactPatterns []*regexp.Regexp
}

// WithLogs turns log export on over the same endpoint, TLS settings, resource
// and shutdown flush as traces and metrics. Only what is written to LogWriter
// is exported, after its filters; the global LoggerProvider is left alone, so
// nothing can reach the collector around them. With no WithLogAllowFields, a
// record carries its time, level, service and message and nothing else.
//
// Logs never go to a collector that isn't authenticated: with
// WithInsecureSkipVerify on they stay off, traces and metrics carry on, and
// ExportState says why.
func WithLogs(enabled bool) Option {
	return func(c *config) { c.logs.enabled = enabled }
}

// WithLogAllowFields names the fields a record may carry, on top of the time,
// level, service and message. Every other field is dropped. Calls add up.
func WithLogAllowFields(names ...string) Option {
	return func(c *config) { c.logs.allow = append(c.logs.allow, names...) }
}

// WithLogClassOnlyFields marks fields to export as their class ("[string]",
// "[number]", "[bool]", "[object]", "[array]" or "[null]") and never their
// text. It narrows WithLogAllowFields and doesn't extend it: a field that isn't
// allowed stays dropped. It also applies to service and message.
func WithLogClassOnlyFields(names ...string) Option {
	return func(c *config) { c.logs.classOnly = append(c.logs.classOnly, names...) }
}

// WithLogDropFields marks fields never to export. It beats WithLogAllowFields
// and WithLogClassOnlyFields, and also applies to service and message.
func WithLogDropFields(names ...string) Option {
	return func(c *config) { c.logs.drop = append(c.logs.drop, names...) }
}

// WithLogMinLevel sets the lowest level exported. The default is info, and so
// is the result of a name that isn't one of the levels. A line with no level, or
// one this package doesn't know, is never exported.
func WithLogMinLevel(level LogLevel) Option {
	return func(c *config) { c.logs.minLevel = level }
}

// WithLogRedactPatterns adds patterns to the built-in redaction, for shapes of
// your own such as a product's token prefix. They can only add to what is
// removed. Calls add up.
func WithLogRedactPatterns(patterns ...*regexp.Regexp) Option {
	return func(c *config) { c.logs.redactPatterns = append(c.logs.redactPatterns, patterns...) }
}

// WithLogRedactValues adds exact values to remove wherever they appear, with
// nothing around them needing to say they are secret. A value shorter than 8
// bytes is ignored, because it would match ordinary words. Calls add up.
func WithLogRedactValues(values ...string) Option {
	return func(c *config) { c.logs.redactValues = append(c.logs.redactValues, values...) }
}

// LogWriter returns an io.Writer that takes JSON log lines, such as zerolog's,
// and exports what its filters let through. Hand it to a logger before Init if
// that suits the startup order: until a WithLogs Init, and again after its
// shutdown, it discards everything.
//
// Write always reports success and doesn't wait on the network, except that a
// fatal or panic line waits up to 2 seconds for the queue to flush, because the
// process ends right after one. A full queue drops the oldest records. A line that isn't a JSON object, has no known level,
// is over 64 KiB, or holds a private key with no END line isn't exported.
func LogWriter() io.Writer { return logWriter{} }

type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	if e := activeLogs.Load(); e != nil {
		for line := range bytes.Lines(p) {
			e.emit(bytes.TrimSpace(line))
		}
	}
	return len(p), nil
}

// activeLogs follows Init the way the global providers do, so a writer built
// before Init starts working once logs are on.
var activeLogs atomic.Pointer[logEmitter]

type logEmitter struct {
	logger    log.Logger
	flush     func(context.Context) error
	minLevel  log.Severity
	allow     map[string]bool
	classOnly map[string]bool
	drop      map[string]bool
	redactor  *logRedactor
}

func newLogEmitter(lp *sdklog.LoggerProvider, cfg logConfig) *logEmitter {
	minLevel, ok := logSeverities[string(cfg.minLevel)]
	if !ok {
		minLevel = log.SeverityInfo
	}
	return &logEmitter{
		logger:    lp.Logger("github.com/Bugs5382/go-otel"),
		flush:     lp.ForceFlush,
		minLevel:  minLevel,
		allow:     nameSet(cfg.allow),
		classOnly: nameSet(cfg.classOnly),
		drop:      nameSet(cfg.drop),
		redactor:  newLogRedactor(cfg.redactValues, cfg.redactPatterns),
	}
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

func (e *logEmitter) emit(line []byte) {
	if len(line) == 0 || len(line) > maxLogLine {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil {
		return
	}
	level := jsonText(fields["level"])
	severity, ok := logSeverities[level]
	if !ok || severity < e.minLevel {
		return
	}

	var record log.Record
	record.SetSeverity(severity)
	record.SetSeverityText(level)
	if at, err := time.Parse(time.RFC3339Nano, jsonText(fields["time"])); err == nil {
		record.SetTimestamp(at)
	}
	for name, raw := range fields {
		if !e.exported(name) {
			continue
		}
		value, ok := e.value(name, raw)
		if !ok {
			return
		}
		if name == "message" {
			record.SetBody(value)
		} else {
			record.AddAttributes(attribute.KeyValue{Key: attribute.Key(name), Value: value})
		}
	}
	e.logger.Emit(correlated(fields), record)
	if severity >= log.SeverityFatal {
		ctx, cancel := context.WithTimeout(context.Background(), fatalFlushWait)
		defer cancel()
		if err := e.flush(ctx); err != nil {
			otel.Handle(err)
		}
	}
}

// exported reports whether a field may reach the record. time, level and the
// trace ids become the record's own fields, so they never pass as attributes.
func (e *logEmitter) exported(name string) bool {
	switch name {
	case "time", "level", "trace_id", "span_id":
		return false
	case "message", "service":
		return !e.drop[name]
	}
	return e.allow[name] && !e.drop[name]
}

// value returns a field as it may leave the process. ok is false when the whole
// record must be withheld. An object or array exports only its class, because
// the names inside it were never allowed.
func (e *logEmitter) value(name string, raw json.RawMessage) (attribute.Value, bool) {
	class := jsonClass(raw)
	if e.classOnly[name] || class == "object" || class == "array" || class == "null" {
		return attribute.StringValue("[" + class + "]"), true
	}
	switch class {
	case "string":
		text, ok := e.redactor.scrub(jsonText(raw))
		return attribute.StringValue(text), ok
	case "bool":
		return attribute.BoolValue(raw[0] == 't'), true
	}
	if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return attribute.Int64Value(n), true
	}
	if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
		return attribute.Float64Value(f), true
	}
	return attribute.StringValue("[number]"), true
}

func jsonClass(raw json.RawMessage) string {
	switch raw[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

// jsonText returns a JSON string's text, or "" for anything else.
func jsonText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

// correlated carries the line's trace_id and span_id to the SDK, which reads a
// record's trace context from the context it is emitted with.
func correlated(fields map[string]json.RawMessage) context.Context {
	traceID, err := trace.TraceIDFromHex(jsonText(fields["trace_id"]))
	if err != nil {
		return context.Background()
	}
	spanID, err := trace.SpanIDFromHex(jsonText(fields["span_id"]))
	if err != nil {
		return context.Background()
	}
	return trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}))
}
