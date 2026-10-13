package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type TracerProviderShutdownFunc func(context.Context) error

// DefaultServiceName is the spans' service when OTEL_SERVICE_NAME is unset.
const DefaultServiceName = "task"

// Enabled reports whether spans are exported: OTEL_TRACES_EXPORTER names an
// exporter (other than "none"), or an OTLP endpoint is set
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_TRACES_ENDPOINT), which
// the OTLP exporter then sends to.
func Enabled() bool {
	switch exporter := os.Getenv("OTEL_TRACES_EXPORTER"); {
	case exporter == "none":
		return false
	case exporter != "":
		return true
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

func Initialize(ctx context.Context) (TracerProviderShutdownFunc, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating span exporter: %w", err)
	}

	// The service is OTEL_SERVICE_NAME, else "task" (not the SDK's
	// "unknown_service:task").
	res, err := resource.Merge(
		resource.NewSchemaless(attribute.String("service.name", DefaultServiceName)),
		resource.Environment(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	tracerProvider := trace.NewTracerProvider(
		trace.WithBatcher(spanExporter),
		trace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tracerProvider.Shutdown, nil
}

// envCarrier is the W3C trace context in environment variables
// (TRACEPARENT, TRACESTATE), as a TextMapCarrier.
type envCarrier map[string]string

func (c envCarrier) Get(key string) string { return c[strings.ToUpper(key)] }
func (c envCarrier) Set(key, value string) { c[strings.ToUpper(key)] = value }
func (c envCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// ParentFromEnv is ctx with the span of TRACEPARENT (and TRACESTATE) as its
// parent, when set: the process that ran Task (e.g. a wrapper recording the
// run, or a CI job) gets Task's spans in its trace.
func ParentFromEnv(ctx context.Context) context.Context {
	traceparent := os.Getenv("TRACEPARENT")
	if traceparent == "" {
		return ctx
	}
	carrier := envCarrier{"TRACEPARENT": traceparent, "TRACESTATE": os.Getenv("TRACESTATE")}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// Environ is environ (nil: this process's) with TRACEPARENT (and
// TRACESTATE) set to the span of ctx, for a command Task runs: a program it
// starts that reads them (another task, any instrumented tool) puts its
// spans under the command's. environ as it is when ctx has no recording
// span.
func Environ(ctx context.Context, environ []string) []string {
	if !oteltrace.SpanContextFromContext(ctx).IsValid() {
		return environ
	}
	if environ == nil {
		environ = os.Environ()
	}
	carrier := envCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	out := make([]string, 0, len(environ)+len(carrier))
	for _, kv := range environ {
		if key, _, _ := strings.Cut(kv, "="); key != "TRACEPARENT" && key != "TRACESTATE" {
			out = append(out, kv)
		}
	}
	for k, v := range carrier {
		out = append(out, k+"="+v)
	}
	return out
}
