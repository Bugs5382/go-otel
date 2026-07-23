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
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestKVBuildsAttr(t *testing.T) {
	a := KV("http.route", "/items/{id}")
	if a.Key != "http.route" || a.Val != "/items/{id}" {
		t.Fatalf("unexpected Attr: %+v", a)
	}
}

func TestToKeyValueTypedValues(t *testing.T) {
	cases := []struct {
		name string
		attr Attr
		want attribute.KeyValue
	}{
		{"string", KV("k", "v"), attribute.String("k", "v")},
		{"bool", KV("k", true), attribute.Bool("k", true)},
		{"int", KV("k", 7), attribute.Int("k", 7)},
		{"int64", KV("k", int64(7)), attribute.Int64("k", 7)},
		{"float64", KV("k", 1.5), attribute.Float64("k", 1.5)},
		{"fallback", KV("k", struct{ X int }{X: 1}), attribute.String("k", "{1}")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toKeyValue(tc.attr)
			if got != tc.want {
				t.Fatalf("toKeyValue(%v) = %v, want %v", tc.attr, got, tc.want)
			}
		})
	}
}

func TestToKeyValuesEmpty(t *testing.T) {
	if got := toKeyValues(nil); got != nil {
		t.Fatalf("expected nil for no attrs, got %v", got)
	}
}
