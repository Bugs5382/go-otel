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
	"fmt"

	"go.opentelemetry.io/otel/attribute"
)

// Attr is a neutral metric attribute: a key paired with an arbitrary value.
// It exists so a caller of CounterMetric/HistogramMetric never has to import
// go.opentelemetry.io/otel/attribute to tag a measurement. Build one with KV.
type Attr struct {
	Key string
	Val any
}

// KV builds an Attr from a key and value. Val accepts string, bool, int,
// int64, float64, and slices of those types directly; any other type is
// rendered with fmt.Sprintf("%v", ...) rather than rejected, so a caller never
// has to special-case an attribute value to stay neutral.
func KV(key string, val any) Attr {
	return Attr{Key: key, Val: val}
}

// toKeyValues converts neutral Attrs to the raw otel attribute.KeyValue the
// SDK instruments require. This is the one seam where Attr crosses into raw
// otel types, and it stays internal to the package.
func toKeyValues(attrs []Attr) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		out[i] = toKeyValue(a)
	}
	return out
}

func toKeyValue(a Attr) attribute.KeyValue {
	switch v := a.Val.(type) {
	case string:
		return attribute.String(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case float64:
		return attribute.Float64(a.Key, v)
	case []string:
		return attribute.StringSlice(a.Key, v)
	case []bool:
		return attribute.BoolSlice(a.Key, v)
	case []int:
		return attribute.IntSlice(a.Key, v)
	case []int64:
		return attribute.Int64Slice(a.Key, v)
	case []float64:
		return attribute.Float64Slice(a.Key, v)
	default:
		return attribute.String(a.Key, fmt.Sprintf("%v", v))
	}
}
