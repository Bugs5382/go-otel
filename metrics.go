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
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// meterName scopes the instruments these helpers create. Consumers who want a
// distinct instrumentation scope can build their own meter via otel.Meter.
const meterName = "github.com/Bugs5382/go-otel"

// Counter returns a monotonic Int64Counter off the global meter. Instrument
// creation only fails on a malformed name, which is a programming error, so the
// helper panics rather than forcing every call site to handle an error that
// cannot occur at runtime.
//
// Counter returns the raw go.opentelemetry.io/otel/metric type, so a caller
// that wants to avoid a raw otel import in its own code should use NewCounter
// instead; it wraps the same instrument behind the neutral CounterMetric
// interface. Counter itself is kept, unchanged, for existing callers.
func Counter(name, description string) metric.Int64Counter {
	c, err := otel.Meter(meterName).Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		panic(fmt.Sprintf("go-otel: create counter %q: %v", name, err))
	}
	return c
}

// Histogram returns a Float64Histogram off the global meter. unit is a UCUM
// string (for example "s" or "ms"); pass "" for no unit. Like Counter, it
// panics on the programming-error case of a malformed name.
//
// Histogram returns the raw go.opentelemetry.io/otel/metric type; a caller
// that wants to avoid a raw otel import in its own code should use
// NewHistogram instead. Histogram itself is kept, unchanged, for existing
// callers.
func Histogram(name, description, unit string) metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(name,
		metric.WithDescription(description),
		metric.WithUnit(unit),
	)
	if err != nil {
		panic(fmt.Sprintf("go-otel: create histogram %q: %v", name, err))
	}
	return h
}

// CounterMetric is a neutral monotonic counter: Add records n against attrs.
// No go.opentelemetry.io/otel type appears in this interface, so a package
// that only depends on CounterMetric (built via NewCounter) never needs a raw
// otel import.
//
// The neutral interfaces are named CounterMetric/HistogramMetric, not
// Counter/Histogram, because Counter and Histogram are already taken by the
// raw-returning functions above; Go does not allow a function and a type to
// share a name in one package. Keeping those functions' names stable is the
// back-compat contract this package makes to existing callers.
type CounterMetric interface {
	Add(ctx context.Context, n int64, attrs ...Attr)
}

// HistogramMetric is a neutral value-distribution recorder: Record adds one
// observation, tagged with attrs. See CounterMetric for why it is not named
// Histogram.
type HistogramMetric interface {
	Record(ctx context.Context, v float64, attrs ...Attr)
}

// counterMetric adapts a raw metric.Int64Counter to CounterMetric, converting
// neutral Attrs to attribute.KeyValue at the call site. Raw otel stays
// internal to this type.
type counterMetric struct{ c metric.Int64Counter }

// histogramMetric adapts a raw metric.Float64Histogram to HistogramMetric.
type histogramMetric struct{ h metric.Float64Histogram }

func (c counterMetric) Add(ctx context.Context, n int64, attrs ...Attr) {
	c.c.Add(ctx, n, metric.WithAttributes(toKeyValues(attrs)...))
}

func (h histogramMetric) Record(ctx context.Context, v float64, attrs ...Attr) {
	h.h.Record(ctx, v, metric.WithAttributes(toKeyValues(attrs)...))
}

// NewCounter builds a neutral CounterMetric off the global meter, wrapping the
// same instrument Counter builds. Call after Init so measurements export.
func NewCounter(name, description string) CounterMetric {
	return counterMetric{c: Counter(name, description)}
}

// NewHistogram builds a neutral HistogramMetric off the global meter, wrapping
// the same instrument Histogram builds. Call after Init so measurements export.
func NewHistogram(name, description, unit string) HistogramMetric {
	return histogramMetric{h: Histogram(name, description, unit)}
}
