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
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const (
	// redactedKey is what is left of a whole private key.
	redactedKey = "[redacted private key]"

	// minRedactValue is the shortest exact value matched: anything shorter is
	// a word, not a secret.
	minRedactValue = 8
)

var (
	terminalEscapes = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b.`)

	privateKeyBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
	privateKey      = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)

	// wholeValueKeys are the headers whose whole value is a credential, and the
	// annotation that repeats a manifest. A header's value has no reliable end,
	// so the rest of the line goes.
	wholeValueKeys = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|x-auth-token|x-vault-token|last-applied-configuration)(\\?["']?\s*[:=]\s*).*$`)

	urlPassword = regexp.MustCompile(`(://[^/\s:@]*):\S*@`)

	// secretKeys is key=value and key: value, quoted or bare, where the key says
	// the value is secret.
	secretKeys = regexp.MustCompile(`(?i)(\\?["']?)([a-z0-9_.\-]*(?:password|passwd|pwd|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|\bkek\b|_kek\b|enc[_-]?key|signature)[a-z0-9_.\-]*)(\\?["']?\s*[:=]\s*)(\\"(?:[^"\\]|\\[^"])*\\"|"(?:[^"\\]|\\.)*"|'[^']*'|[^\s,;&}\])]+)`)

	tokenShapes = []*regexp.Regexp{
		regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+(?:\.[A-Za-z0-9_\-]*)?`),
		regexp.MustCompile(`(?i)\bage-secret-key-1[a-z0-9]+`),
		regexp.MustCompile(`\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]+`),
		regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=\-]+`),
		regexp.MustCompile(`\b[Bb]asic\s+[A-Za-z0-9+/]{8,}={0,2}`),
	}
)

// logRedactor removes secrets from the text a record exports. It holds no
// state between calls.
type logRedactor struct {
	values   []string
	patterns []*regexp.Regexp
}

func newLogRedactor(values []string, patterns []*regexp.Regexp) *logRedactor {
	r := &logRedactor{}
	for _, value := range values {
		if len(value) >= minRedactValue {
			r.values = append(r.values, value)
		}
	}
	// Longest first, so a value that contains another is removed whole.
	slices.SortFunc(r.values, func(a, b string) int { return len(b) - len(a) })
	for _, pattern := range patterns {
		if pattern != nil {
			r.patterns = append(r.patterns, pattern)
		}
	}
	return r
}

// scrub returns text as it may be exported. ok is false for a private key with
// no END line: where it stops can't be known, so the record is withheld whole.
func (r *logRedactor) scrub(text string) (string, bool) {
	text = printable(text)
	text = privateKey.ReplaceAllString(text, redactedKey)
	if privateKeyBegin.MatchString(text) {
		return "", false
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = r.scrubLine(line)
	}
	return strings.Join(lines, "\n"), true
}

func (r *logRedactor) scrubLine(line string) string {
	for _, value := range r.values {
		line = strings.ReplaceAll(line, value, redactedValue)
	}
	line = wholeValueKeys.ReplaceAllString(line, "${1}${2}"+redactedValue)
	line = urlPassword.ReplaceAllString(line, "${1}:"+redactedValue+"@")
	line = secretKeys.ReplaceAllStringFunc(line, func(match string) string {
		parts := secretKeys.FindStringSubmatch(match)
		if namesASecret(parts[2]) {
			return match
		}
		return parts[1] + parts[2] + parts[3] + redactedValue
	})
	for _, shape := range tokenShapes {
		line = shape.ReplaceAllString(line, redactedValue)
	}
	for _, pattern := range r.patterns {
		line = pattern.ReplaceAllString(line, redactedValue)
	}
	return line
}

// namesASecret is true for a key that names a secret instead of holding one
// (secretName, secretKeyRef, token_type): the name helps a diagnosis.
func namesASecret(key string) bool {
	key = strings.ToLower(key)
	return strings.HasSuffix(key, "name") || strings.HasSuffix(key, "ref") || strings.HasSuffix(key, "type")
}

// printable takes out terminal escapes, control characters and bytes that
// aren't text. Tabs and newlines stay.
func printable(s string) string {
	s = terminalEscapes.ReplaceAllString(strings.ToValidUTF8(s, ""), "")
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\t' && c != '\n' {
			return -1
		}
		return c
	}, s)
}
