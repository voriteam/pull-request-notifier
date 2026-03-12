package telemetry

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Init initializes OTel trace and log providers if OTEL_EXPORTER_OTLP_ENDPOINT is set.
// It configures slog to send logs via both JSON stdout and OTLP.
// Returns a shutdown function that should be deferred.
func Init(ctx context.Context, serviceName, version string) func(context.Context) error {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		slog.Info("telemetry disabled (OTEL_EXPORTER_OTLP_ENDPOINT not set)")
		return func(context.Context) error { return nil }
	}

	// Surface OTel SDK errors (e.g. export failures) via slog instead of Go's log package.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("otel.sdk.error", "err", err)
	}))

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	)

	// Traces
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		slog.Error("failed to create trace exporter", "err", err)
		return func(context.Context) error { return nil }
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Logs
	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		slog.Error("failed to create log exporter", "err", err)
		// Traces are still usable, so don't bail out entirely.
		slog.Info("telemetry enabled (traces only)", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
		return tp.Shutdown
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)

	// Fan out slog to both stdout (JSON) and OTLP.
	otelHandler := otelslog.NewHandler(serviceName, otelslog.WithLoggerProvider(lp))
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(fanoutHandler{jsonHandler, otelHandler}))

	slog.Info("telemetry enabled (traces + logs)", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

	return func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return err
		}
		return lp.Shutdown(ctx)
	}
}

// fanoutHandler sends log records to multiple slog handlers.
type fanoutHandler struct {
	primary   slog.Handler
	secondary slog.Handler
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return f.primary.Enabled(ctx, level) || f.secondary.Enabled(ctx, level)
}

func (f fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	if err := f.primary.Handle(ctx, record); err != nil {
		return err
	}
	return f.secondary.Handle(ctx, record)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanoutHandler{f.primary.WithAttrs(attrs), f.secondary.WithAttrs(attrs)}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	return fanoutHandler{f.primary.WithGroup(name), f.secondary.WithGroup(name)}
}

// Ensure fanoutHandler implements slog.Handler.
var _ slog.Handler = fanoutHandler{}

