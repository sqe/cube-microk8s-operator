package telemetry

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDurationFromEnvironment(t *testing.T) {
	const name = "CUBE_TEST_DURATION"
	t.Cleanup(func() { _ = os.Unsetenv(name) })
	if got, err := durationFromEnvironment(name, time.Minute); err != nil || got != time.Minute {
		t.Fatalf("fallback = %s, %v; want 1m, nil", got, err)
	}
	if err := os.Setenv(name, "15s"); err != nil {
		t.Fatal(err)
	}
	if got, err := durationFromEnvironment(name, time.Minute); err != nil || got != 15*time.Second {
		t.Fatalf("parsed = %s, %v; want 15s, nil", got, err)
	}
	if err := os.Setenv(name, "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := durationFromEnvironment(name, time.Minute); err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("error = %v, want variable name", err)
	}
	if err := os.Setenv(name, "-1s"); err != nil {
		t.Fatal(err)
	}
	if _, err := durationFromEnvironment(name, time.Minute); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("error = %v, want positive-duration validation", err)
	}
}
