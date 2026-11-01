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
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const logTime = "2026-10-31T12:00:00Z"

// jsonLine renders the shape go-log writes, with extra as raw JSON fields.
func jsonLine(level, message, extra string) string {
	line := fmt.Sprintf(`{"level":%q,"service":"billing","time":%q`, level, logTime)
	if extra != "" {
		line += "," + extra
	}
	return line + fmt.Sprintf(`,"message":%q}`, message) + "\n"
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// write sends each line through LogWriter and fails on any short or failed
// write, which the logger on the other side would see.
func write(t *testing.T, lines ...string) {
	t.Helper()
	w := LogWriter()
	for _, line := range lines {
		n, err := w.Write([]byte(line))
		if err != nil || n != len(line) {
			t.Fatalf("Write returned %d, %v; want %d, nil", n, err, len(line))
		}
	}
}

// exported returns every log record the collector holds, and its whole wire
// content as text, so a canary can't hide in a field a test forgot to read.
func (c *otlpCollector) exported() ([]*logspb.LogRecord, string) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	var records []*logspb.LogRecord
	var wire strings.Builder
	for _, req := range c.logReqs {
		wire.WriteString(req.String())
		for _, rl := range req.GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				records = append(records, sl.GetLogRecords()...)
			}
		}
	}
	return records, wire.String()
}

// collectLogs runs lines through a fresh pipeline and flushes it.
func collectLogs(t *testing.T, opts []Option, lines ...string) ([]*logspb.LogRecord, string) {
	t.Helper()
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	shutdown, err := InitWithOptions(context.Background(), "test-service", c.addr, opts...)
	if err != nil {
		t.Fatalf("InitWithOptions returned error: %v", err)
	}
	write(t, lines...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	return c.exported()
}

func oneRecord(t *testing.T, records []*logspb.LogRecord) *logspb.LogRecord {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1: %v", len(records), records)
	}
	return records[0]
}

func attrs(r *logspb.LogRecord) map[string]string {
	out := make(map[string]string)
	for _, kv := range r.GetAttributes() {
		out[kv.GetKey()] = kv.GetValue().String()
	}
	return out
}

func assertAttrKeys(t *testing.T, r *logspb.LogRecord, want ...string) {
	t.Helper()
	got := attrs(r)
	if len(got) != len(want) {
		t.Fatalf("attributes %v, want only %v", got, want)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("attributes %v, want only %v", got, want)
		}
	}
}

func assertNoCanary(t *testing.T, wire string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(wire, canary) {
			t.Errorf("collector received %q", canary)
		}
	}
}

func TestLogsAreOffUnlessEnabled(t *testing.T) {
	for name, opts := range map[string][]Option{
		"no option":  nil,
		"false":      {WithLogs(false)},
		"only allow": {WithLogAllowFields("user")},
	} {
		t.Run(name, func(t *testing.T) {
			records, _ := collectLogs(t, opts, jsonLine("error", "not exported", ""))
			if len(records) != 0 {
				t.Fatalf("got %d records with logs off", len(records))
			}
			if want := (SignalStatus{Disabled: logsNotEnabled}); ExportState().Logs != want {
				t.Fatalf("log state with logs off: %+v", ExportState().Logs)
			}
		})
	}
}

func TestLogsDefaultExportsOnlyCoreFields(t *testing.T) {
	const canary = "unknown-field-canary"
	records, wire := collectLogs(t, []Option{WithLogs(true)}, jsonLine("warn", "disk nearly full",
		`"user":"`+canary+`","error":"`+canary+`","caller":"`+canary+`","nested":{"a":"`+canary+`"},"list":["`+canary+`"],"count":7`))

	r := oneRecord(t, records)
	assertAttrKeys(t, r, "service")
	if got := r.GetAttributes()[0].GetValue().GetStringValue(); got != "billing" {
		t.Errorf("service = %q", got)
	}
	if got := r.GetBody().GetStringValue(); got != "disk nearly full" {
		t.Errorf("body = %q", got)
	}
	if r.GetSeverityNumber() != logspb.SeverityNumber_SEVERITY_NUMBER_WARN || r.GetSeverityText() != "warn" {
		t.Errorf("severity = %v %q", r.GetSeverityNumber(), r.GetSeverityText())
	}
	want, _ := time.Parse(time.RFC3339, logTime)
	if got := time.Unix(0, int64(r.GetTimeUnixNano())); !got.Equal(want) {
		t.Errorf("time = %v, want %v", got, want)
	}
	assertNoCanary(t, wire, canary)
	if !strings.Contains(wire, "test-service") {
		t.Error("record lost the shared resource's service.name")
	}
}

func TestLogsAllowListKeepsOnlyNamedFields(t *testing.T) {
	const canary = "unknown-field-canary"
	records, wire := collectLogs(t,
		[]Option{WithLogs(true), WithLogAllowFields("route"), WithLogAllowFields("status", "ratio", "ok", "missing")},
		jsonLine("info", "served", `"route":"/orders","status":200,"ratio":0.5,"ok":true,"user":"`+canary+`"`))

	r := oneRecord(t, records)
	assertAttrKeys(t, r, "service", "route", "status", "ratio", "ok")
	for _, kv := range r.GetAttributes() {
		switch kv.GetKey() {
		case "route":
			if kv.GetValue().GetStringValue() != "/orders" {
				t.Errorf("route = %v", kv.GetValue())
			}
		case "status":
			if kv.GetValue().GetIntValue() != 200 {
				t.Errorf("status = %v", kv.GetValue())
			}
		case "ratio":
			if kv.GetValue().GetDoubleValue() != 0.5 {
				t.Errorf("ratio = %v", kv.GetValue())
			}
		case "ok":
			if !kv.GetValue().GetBoolValue() {
				t.Errorf("ok = %v", kv.GetValue())
			}
		}
	}
	assertNoCanary(t, wire, canary)
}

func TestLogsCompositeValuesExportOnlyTheirClass(t *testing.T) {
	const canary = "nested-canary"
	records, wire := collectLogs(t, []Option{WithLogs(true), WithLogAllowFields("req", "tags", "none")},
		jsonLine("info", "m", `"req":{"password":"`+canary+`"},"tags":["`+canary+`"],"none":null`))

	got := make(map[string]string)
	for _, kv := range oneRecord(t, records).GetAttributes() {
		got[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	for key, want := range map[string]string{"req": "[object]", "tags": "[array]", "none": "[null]"} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	assertNoCanary(t, wire, canary)
}

func TestLogsFieldMarksNarrowTheAllowList(t *testing.T) {
	const canary = "marked-canary"
	records, wire := collectLogs(t,
		[]Option{
			WithLogs(true),
			WithLogAllowFields("kind", "user", "count", "both"),
			WithLogClassOnlyFields("user", "count", "both", "unlisted"),
			WithLogDropFields("kind", "both", "service"),
		},
		jsonLine("info", "m", `"kind":"`+canary+`","user":"`+canary+`","count":987654321987,"both":"`+canary+`","unlisted":"`+canary+`"`))

	r := oneRecord(t, records)
	assertAttrKeys(t, r, "user", "count")
	got := make(map[string]string)
	for _, kv := range r.GetAttributes() {
		got[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if got["user"] != "[string]" || got["count"] != "[number]" {
		t.Errorf("class-only values = %v", got)
	}
	assertNoCanary(t, wire, canary, "987654321987")
}

func TestLogsMarksReachTheMessage(t *testing.T) {
	const canary = "message-canary"
	for name, tc := range map[string]struct {
		opt  Option
		want string
	}{
		"class-only": {WithLogClassOnlyFields("message"), "[string]"},
		"dropped":    {WithLogDropFields("message"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			records, wire := collectLogs(t, []Option{WithLogs(true), tc.opt}, jsonLine("info", canary, ""))
			if got := oneRecord(t, records).GetBody().GetStringValue(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			assertNoCanary(t, wire, canary)
		})
	}
}

// The planted values are invented; none of them is a key anywhere.
const (
	plantPassword = "pl4nted-" + "hunter2"
	plantToken    = "pl4ntedT0kenValue99"
	plantJWT      = "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJwbGFudGVkIn0" + "." + "c2lnbmF0dXJlLXBsYW50ZWQ"
	plantAge      = "AGE-SECRET-KEY-1QYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGPQYQSZQGP"
	plantBasic    = "dXNlcjpwbDRudGVk"
	plantKnown    = "9f3a7c1e5b2d4860a1c3e5f7091b3d5f"
	plantPEMBody  = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7"
)

// pemLine builds a key's BEGIN or END line, so the source holds nothing a
// secret scanner takes for a key.
func pemLine(edge, kind string) string {
	return "-----" + edge + " " + kind + "PRIVATE " + "KEY-----"
}

// textLine puts text in both the message and an allowed field.
func textLine(text string) string {
	return fmt.Sprintf(`{"level":"info","service":"billing","time":%q,"detail":%s,"message":%s}`+"\n",
		logTime, jsonString(text), jsonString(text))
}

func exportedTexts(t *testing.T, records []*logspb.LogRecord) []string {
	t.Helper()
	r := oneRecord(t, records)
	texts := []string{r.GetBody().GetStringValue()}
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == "detail" {
			texts = append(texts, kv.GetValue().GetStringValue())
		}
	}
	if len(texts) != 2 {
		t.Fatalf("detail field missing: %v", r)
	}
	return texts
}

func TestLogRedactionRemovesEverySecretShape(t *testing.T) {
	cases := map[string]struct{ in, gone string }{
		"logfmt password":   {"level=info msg=connect password=" + plantPassword + " host=db", plantPassword},
		"bare token":        {"token=" + plantToken + " ok", plantToken},
		"secret key":        {"client_secret=" + plantToken, plantToken},
		"quoted value":      {`password="` + plantPassword + ` with spaces" next=1`, "with spaces"},
		"bearer header":     {"GET /x Authorization: Bearer " + plantToken, plantToken},
		"lower bearer":      {"sent bearer " + plantToken + " upstream", plantToken},
		"basic header":      {"Authorization: Basic " + plantBasic, plantBasic},
		"bare basic":        {"proxy sent Basic " + plantBasic + " upstream", plantBasic},
		"cookie":            {"Cookie: session=" + plantToken + "; theme=dark", plantToken},
		"set-cookie":        {"Set-Cookie: sid=" + plantToken + "; HttpOnly", plantToken},
		"api key header":    {"X-Api-Key: " + plantToken, plantToken},
		"jwt":               {"verified " + plantJWT + " for alice", plantJWT},
		"jwt two parts":     {"cut eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJwbGFudGVkIn0", "eyJzdWIiOiJwbGFudGVkIn0"},
		"github token":      {"fetched with ghp_pl4ntedGithubT0ken123", "ghp_pl4ntedGithubT0ken123"},
		"url password":      {"dial postgres://app:" + plantPassword + "@db:5432/x", plantPassword},
		"url empty user":    {"dial redis://:" + plantPassword + "@cache:6379/0", plantPassword},
		"url slash":         {"dial postgres://u:ab/" + plantPassword + "@db:5432/x", plantPassword},
		"url at sign":       {"dial postgres://u:ab@" + plantPassword + "@db:5432/x", plantPassword},
		"json secret field": {`{"level":"info","password":"` + plantPassword + `","ok":true}`, plantPassword},
		"json nested token": {`{"auth":{"access_token":"` + plantToken + `"}}`, plantToken},
		"json escaped":      {`{"msg":"body {\"password\":\"` + plantPassword + `\"}"}`, plantPassword},
		"yaml dump":         {"  smtpPassword: " + plantPassword, plantPassword},
		"env assignment":    {"VAULT_ROOT_KEK=" + plantToken, plantToken},
		"env enc key":       {"TOTP_ENC_KEY=" + plantToken, plantToken},
		"dsn query":         {"dsn: postgres://u@db/x?password=" + plantPassword, plantPassword},
		"age key":           {"key is " + plantAge, plantAge},
		"age key lower":     {"key is " + strings.ToLower(plantAge), strings.ToLower(plantAge)},
		"last applied":      {`kubectl.kubernetes.io/last-applied-configuration: {"spec":{"value":"` + plantToken + `"}}`, plantToken},
		"command line":      {"      --password=" + plantPassword, plantPassword},
		"pem one line":      {`{"key":"` + pemLine("BEGIN", "RSA ") + `\n` + plantPEMBody + `\n` + pemLine("END", "RSA ") + `"}`, plantPEMBody},
		"known value":       {"ready " + plantKnown + " done", plantKnown},
		"known in url":      {"http://x/" + plantKnown + "/y", plantKnown},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			records, wire := collectLogs(t,
				[]Option{WithLogs(true), WithLogAllowFields("detail"), WithLogRedactValues(plantKnown)},
				textLine(c.in))
			exportedTexts(t, records)
			assertNoCanary(t, wire, c.gone)
		})
	}
}

func TestLogRedactionRemovesAMultiLinePrivateKey(t *testing.T) {
	in := "before\n" + pemLine("BEGIN", "OPENSSH ") + "\n" + plantPEMBody + "\n" + plantPEMBody + "\n" + pemLine("END", "OPENSSH ") + "\nafter"
	records, wire := collectLogs(t, []Option{WithLogs(true), WithLogAllowFields("detail")}, textLine(in))
	assertNoCanary(t, wire, plantPEMBody)
	for _, got := range exportedTexts(t, records) {
		if got != "before\n[redacted private key]\nafter" {
			t.Fatalf("got %q", got)
		}
	}
}

// A key with no END line withholds the whole record: it fails closed.
func TestAnUnterminatedPrivateKeyWithholdsTheRecord(t *testing.T) {
	for name, cut := range map[string]string{
		"many lines":     pemLine("BEGIN", "") + "\n" + plantPEMBody + "\nmore " + plantPEMBody,
		"one line":       `{"key":"` + pemLine("BEGIN", "RSA ") + `\n` + plantPEMBody,
		"after a whole":  pemLine("BEGIN", "") + "\na\n" + pemLine("END", "") + "\n" + pemLine("BEGIN", "") + "\n" + plantPEMBody,
		"behind escapes": "-----BEGIN \x1b[0mPRIVATE " + "KEY-----\n" + plantPEMBody,
	} {
		t.Run(name, func(t *testing.T) {
			for label, line := range map[string]string{
				"message": jsonLine("info", cut, ""),
				"field":   jsonLine("info", "harmless", `"detail":`+jsonString(cut)),
			} {
				records, wire := collectLogs(t, []Option{WithLogs(true), WithLogAllowFields("detail")},
					line, jsonLine("info", "next", ""))
				if got := oneRecord(t, records).GetBody().GetStringValue(); got != "next" {
					t.Fatalf("%s: got %q", label, got)
				}
				assertNoCanary(t, wire, plantPEMBody, "harmless")
			}
		})
	}
}

func TestLogRedactionAddsCallerPatterns(t *testing.T) {
	records, wire := collectLogs(t,
		[]Option{WithLogs(true), WithLogAllowFields("detail"),
			WithLogRedactPatterns(regexp.MustCompile(`acct-[0-9]+`), nil), WithLogRedactPatterns(regexp.MustCompile(`tok_[a-z]+`))},
		textLine("charged acct-12345 by tok_abc with token="+plantToken))
	exportedTexts(t, records)
	assertNoCanary(t, wire, "acct-12345", "tok_abc", plantToken)
}

// A short known value is ignored, so a word such as "no" never goes.
func TestLogRedactionIgnoresShortKnownValues(t *testing.T) {
	const text = "no change, nothing to do"
	records, _ := collectLogs(t, []Option{WithLogs(true), WithLogRedactValues("no", "")}, jsonLine("info", text, ""))
	if got := oneRecord(t, records).GetBody().GetStringValue(); got != text {
		t.Fatalf("got %q", got)
	}
}

// Ids and diagnosis text look like secrets only to a careless pattern.
func TestLogRedactionKeepsWhatDiagnosisNeeds(t *testing.T) {
	for _, line := range []string{
		"order 1234 shipped to warehouse-7 after 3 retries at 10:42 via http://192.0.2.1:8080/items?limit=5",
		"request 7c9e6679-7425-40de-944b-e07fc1f90ae7 done",
		"trace 4bf92f3577b34da6a3ce929d0e0e4736 span 00f067aa0ba902b7",
		"Warning  BackOff  pod/app-0  Back-off restarting failed container",
		"image: example.org/vault@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"    SecretName:  app-box",
		"token_type=bearer",
		"dial tcp 192.0.2.10:5432: connect: connection refused",
		"/dev/mapper/state on /var/lib/app type ext4 (rw,relatime)",
	} {
		records, _ := collectLogs(t, []Option{WithLogs(true)}, jsonLine("info", line, ""))
		if got := oneRecord(t, records).GetBody().GetStringValue(); got != line {
			t.Fatalf("%q became %q", line, got)
		}
	}
}

// A log line can carry terminal escapes that would act on a reader's terminal.
func TestLogControlCharactersAreRemoved(t *testing.T) {
	records, _ := collectLogs(t, []Option{WithLogs(true)},
		textLine("a\x1b[31mred\x1b[0m\x1b]0;title\x07 b\x00c\rd\te"))
	got := oneRecord(t, records).GetBody().GetStringValue()
	if strings.ContainsAny(got, "\x1b\x00\r\x07") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "ared") || !strings.Contains(got, "\te") {
		t.Fatalf("the text went too: %q", got)
	}
}

func TestLogsMinLevel(t *testing.T) {
	levels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	var lines []string
	for _, level := range levels {
		lines = append(lines, jsonLine(level, level, ""))
	}
	unlevelled := []string{
		`{"message":"no level","time":"` + logTime + `"}` + "\n",
		jsonLine("notice", "unknown level", ""),
		`{"level":7,"message":"bad level"}` + "\n",
	}
	for name, tc := range map[string]struct {
		opts []Option
		want int
	}{
		"default floor": {nil, 5},
		"lowered":       {[]Option{WithLogMinLevel(LogLevelDebug)}, 6},
		"trace":         {[]Option{WithLogMinLevel(LogLevelTrace)}, 7},
		"raised":        {[]Option{WithLogMinLevel(LogLevelWarn)}, 4},
		"highest":       {[]Option{WithLogMinLevel(LogLevelError)}, 3},
		"unknown name":  {[]Option{WithLogMinLevel("loud")}, 5},
	} {
		t.Run(name, func(t *testing.T) {
			opts := append([]Option{WithLogs(true)}, tc.opts...)
			records, _ := collectLogs(t, opts, append(unlevelled, lines...)...)
			if len(records) != tc.want {
				t.Fatalf("got %d records, want %d", len(records), tc.want)
			}
			for _, r := range records {
				if !strings.Contains(strings.Join(levels, " "), r.GetBody().GetStringValue()) {
					t.Errorf("unlevelled line exported: %q", r.GetBody().GetStringValue())
				}
			}
		})
	}
}

func TestLogsCarryTraceCorrelation(t *testing.T) {
	const traceID, spanID = "0102030405060708090a0b0c0d0e0f10", "a1a2a3a4a5a6a7a8"
	records, _ := collectLogs(t, []Option{WithLogs(true), WithLogAllowFields("trace_id", "span_id")},
		jsonLine("info", "m", `"trace_id":"`+traceID+`","span_id":"`+spanID+`"`))

	r := oneRecord(t, records)
	if got := hex.EncodeToString(r.GetTraceId()); got != traceID {
		t.Errorf("trace id = %q", got)
	}
	if got := hex.EncodeToString(r.GetSpanId()); got != spanID {
		t.Errorf("span id = %q", got)
	}
	assertAttrKeys(t, r, "service")
}

func TestLogsIgnoreBadCorrelationAndTime(t *testing.T) {
	const canary = "bad-id-canary"
	records, wire := collectLogs(t, []Option{WithLogs(true)},
		`{"level":"info","time":"`+canary+`","trace_id":"`+canary+`","span_id":"`+canary+`","message":"m"}`+"\n")

	r := oneRecord(t, records)
	if len(r.GetTraceId()) != 0 || len(r.GetSpanId()) != 0 {
		t.Errorf("bad ids became correlation: %x %x", r.GetTraceId(), r.GetSpanId())
	}
	if r.GetTimeUnixNano() != 0 || r.GetObservedTimeUnixNano() == 0 {
		t.Errorf("time = %d, observed = %d", r.GetTimeUnixNano(), r.GetObservedTimeUnixNano())
	}
	assertNoCanary(t, wire, canary)
}

func TestLogWriterDropsWhatItCannotRead(t *testing.T) {
	const canary = "plain-text-canary"
	records, wire := collectLogs(t, []Option{WithLogs(true)},
		"12:00:00 INF password "+canary+"\n",
		`{"level":"info","message":"`+canary,
		`["info","`+canary+`"]`+"\n",
		"\n",
		"",
		jsonLine("info", strings.Repeat("a ", 40*1024)+canary, ""),
	)
	if len(records) != 0 {
		t.Fatalf("got %d records", len(records))
	}
	assertNoCanary(t, wire, canary)
}

func TestLogWriterHandlesSeveralLinesInOneWrite(t *testing.T) {
	records, _ := collectLogs(t, []Option{WithLogs(true)}, jsonLine("info", "one", "")+jsonLine("warn", "two", ""))
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
}

func TestLogWriterIsANoOpOutsideInit(t *testing.T) {
	const early, late = "written-before-init", "written-after-shutdown"
	write(t, jsonLine("error", early, ""))

	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	shutdown, err := InitWithOptions(context.Background(), "test-service", c.addr, WithLogs(true))
	if err != nil {
		t.Fatalf("InitWithOptions returned error: %v", err)
	}
	w := LogWriter()
	write(t, jsonLine("error", "during", ""))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	line := jsonLine("error", late, "")
	if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write after shutdown returned %d, %v", n, err)
	}

	r, wire := c.exported()
	if oneRecord(t, r).GetBody().GetStringValue() != "during" {
		t.Fatalf("unexpected record: %v", r)
	}
	assertNoCanary(t, wire, early, late)
}

func TestLogWriterIsANoOpWithoutCollector(t *testing.T) {
	initForTest(t, "", WithLogs(true))
	write(t, jsonLine("error", "dropped", ""))
	if s := ExportState(); s.Configured || s.Logs != (SignalStatus{}) {
		t.Fatalf("state with no collector: %+v", s)
	}
}

func TestLogWriterFollowsTheLatestInit(t *testing.T) {
	first := &otlpCollector{}
	first.serve(t, "127.0.0.1:0", nil)
	initForTest(t, first.addr, WithLogs(true))
	second := &otlpCollector{}
	second.serve(t, "127.0.0.1:0", nil)
	initForTest(t, second.addr)

	write(t, jsonLine("error", "logs now off", ""))
	if want := (SignalStatus{Disabled: logsNotEnabled}); ExportState().Logs != want {
		t.Fatalf("log state = %+v", ExportState().Logs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The first pipeline stays open until its own shutdown, so give it a batch
	// interval to prove nothing went to it.
	<-ctx.Done()
	if records, _ := first.exported(); len(records) != 0 {
		t.Fatalf("old pipeline got %d records", len(records))
	}
}

func TestLogWriterNeverBlocksOnACollectorThatHangs(t *testing.T) {
	release := make(chan struct{})
	c := &otlpCollector{reject: func(context.Context) error {
		<-release
		return status.Error(codes.Unavailable, "released")
	}}
	c.serve(t, "127.0.0.1:0", nil)
	defer close(release)
	initForTest(t, c.addr, WithLogs(true))

	line := jsonLine("info", "flood", "")
	start := time.Now()
	for range 20000 {
		write(t, line)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("20000 writes took %v with a stuck collector", elapsed)
	}
}

func TestLogWriterConcurrentUse(t *testing.T) {
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, c.addr, WithLogs(true))

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := LogWriter()
			for range 200 {
				_, _ = w.Write([]byte(jsonLine("info", "busy", "")))
				_ = ExportState()
			}
		})
	}
	wg.Go(func() {
		for range 3 {
			initForTest(t, c.addr, WithLogs(true))
		}
	})
	wg.Wait()
}

func TestExportStateReportsLogDelivery(t *testing.T) {
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, c.addr, WithLogs(true))

	write(t, jsonLine("info", "first", ""))
	waitForLogSuccess(t, time.Time{})
	before := ExportState().Logs
	if before.LastError != "" {
		t.Fatalf("unexpected error: %+v", before)
	}

	c.srv.Stop()
	deadline := time.Now().Add(20 * time.Second)
	for ExportState().Logs.LastErrorAt.IsZero() {
		write(t, jsonLine("info", "lost", ""))
		if time.Now().After(deadline) {
			t.Fatalf("outage not recorded: %+v", ExportState().Logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	down := ExportState().Logs
	if !down.LastErrorAt.After(before.LastSuccess) || down.LastSuccess != before.LastSuccess {
		t.Fatalf("outage state = %+v, before = %+v", down, before)
	}

	c.serve(t, c.addr, nil)
	waitForLogSuccess(t, down.LastErrorAt)
	if ExportState().Logs.LastError == "" {
		t.Fatal("recovery cleared the last error, want it kept with its time")
	}
}

// waitForLogSuccess keeps writing until a log export succeeds after since.
func waitForLogSuccess(t *testing.T, since time.Time) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ExportState().Logs.LastSuccess.After(since) {
		write(t, jsonLine("info", "again", ""))
		if time.Now().After(deadline) {
			t.Fatalf("no log export succeeded: %+v", ExportState().Logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestExportStateLogErrorsCarryNoSecrets(t *testing.T) {
	const headerSecret, urlSecret = "header-s3cret value", "url-s3cret"
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "authorization=header-s3cret%20value")

	c := &otlpCollector{reject: func(ctx context.Context) error {
		md, _ := metadata.FromIncomingContext(ctx)
		return status.Errorf(codes.Unauthenticated, "bad token %q from https://collector:%s@192.0.2.1:4317",
			md.Get("authorization"), urlSecret)
	}}
	c.serve(t, "127.0.0.1:0", nil)
	initForTest(t, "http://collector:"+urlSecret+"@"+c.addr, WithLogs(true))

	deadline := time.Now().Add(10 * time.Second)
	for ExportState().Logs.LastError == "" {
		write(t, jsonLine("info", "rejected", ""))
		if time.Now().After(deadline) {
			t.Fatal("no log error recorded")
		}
		time.Sleep(100 * time.Millisecond)
	}
	msg := ExportState().Logs.LastError
	if !strings.Contains(msg, "bad token") {
		t.Errorf("error lost its detail: %q", msg)
	}
	assertNoCanary(t, msg, headerSecret, "header-s3cret%20value", urlSecret)
}

func TestLogsReachAVerifiedCollectorOnly(t *testing.T) {
	cert := selfSignedCert(t)
	c := &otlpCollector{}
	c.serve(t, "127.0.0.1:0", cert)
	endpoint := "https://" + c.addr

	t.Run("an untrusted certificate refuses", func(t *testing.T) {
		initForTest(t, endpoint, WithLogs(true))
		deadline := time.Now().Add(10 * time.Second)
		for ExportState().Logs.LastError == "" {
			write(t, jsonLine("info", "refused", ""))
			if time.Now().After(deadline) {
				t.Fatal("no log error recorded")
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !strings.Contains(ExportState().Logs.LastError, "certificate") {
			t.Fatalf("error = %q", ExportState().Logs.LastError)
		}
		if records, _ := c.exported(); len(records) != 0 {
			t.Fatalf("collector got %d records", len(records))
		}
	})

	t.Run("a trusted certificate delivers", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
		if err := os.WriteFile(caFile, block, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
		initForTest(t, endpoint, WithLogs(true))
		waitForLogSuccess(t, time.Time{})
		if records, _ := c.exported(); len(records) == 0 {
			t.Fatal("collector got no records")
		}
	})
}

func TestSkipVerifyKeepsLogsOff(t *testing.T) {
	const canary = "unverified-canary"
	for name, serve := range map[string]func(*otlpCollector){
		"https": func(c *otlpCollector) { c.serve(t, "127.0.0.1:0", selfSignedCert(t)) },
		"plain": func(c *otlpCollector) { c.serve(t, "127.0.0.1:0", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			c := &otlpCollector{}
			serve(c)
			endpoint := "http://" + c.addr
			if name == "https" {
				endpoint = "https://" + c.addr
			}
			initForTest(t, endpoint, WithLogs(true), WithInsecureSkipVerify(true))

			write(t, jsonLine("error", canary, ""))
			emit(t)
			time.Sleep(1200 * time.Millisecond) // one log batch interval

			s := ExportState()
			if c.spans.Load() == 0 || c.metrics.Load() == 0 {
				t.Fatalf("traces and metrics stopped: %+v", s)
			}
			if want := (SignalStatus{Disabled: logsUnverified}); s.Logs != want {
				t.Fatalf("log state = %+v", s.Logs)
			}
			if !strings.Contains(s.Logs.Disabled, "certificate") {
				t.Fatalf("reason = %q", s.Logs.Disabled)
			}
			records, wire := c.exported()
			if len(records) != 0 {
				t.Fatalf("collector got %d records", len(records))
			}
			assertNoCanary(t, wire, canary)
		})
	}
}
