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
