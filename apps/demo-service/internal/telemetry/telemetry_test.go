package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupInstallsProvidersAndShutsDownCleanly(t *testing.T) {
	// "none" keeps the test offline while exercising the full wiring.
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")
	t.Setenv("OTEL_SERVICE_NAME", "")

	shutdown, err := Setup(context.Background(), "demo-service", "test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	_, span := otel.Tracer("test").Start(context.Background(), "probe")
	if !span.SpanContext().IsValid() {
		t.Error("the global tracer provider does not produce valid spans")
	}
	span.End()

	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
