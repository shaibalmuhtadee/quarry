package handlers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestEchoAcceptsEveryJSONShape(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`null`, `true`, `42`, `"text"`, `[1,2]`, `{"key":"value"}`} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			payload, err := domain.ParsePayload([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Echo(context.Background(), payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(result.JSON()); got != raw {
				t.Fatalf("result = %s, want %s", got, raw)
			}
		})
	}
}

func TestPayloadSizeCountsReceivedJSONBytes(t *testing.T) {
	t.Parallel()

	payload, err := domain.ParsePayload([]byte(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := PayloadSize(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.JSON()), `{"bytes":19}`; got != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
}

func TestSleepWaitsForRequestedDuration(t *testing.T) {
	t.Parallel()

	payload, err := domain.ParsePayload([]byte(`{"duration_ms":15}`))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	result, err := Sleep(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed < 15*time.Millisecond {
		t.Fatalf("sleep returned after %s, want at least 15ms", elapsed)
	}
	if got, want := string(result.JSON()), `{"slept_ms":15}`; got != want {
		t.Fatalf("result = %s, want %s", got, want)
	}
}

func TestSleepStopsWhenContextIsCanceled(t *testing.T) {
	t.Parallel()

	payload, err := domain.ParsePayload([]byte(`{"duration_ms":60000}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	_, err = Sleep(ctx, payload)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("canceled sleep returned after %s, want under one second", elapsed)
	}
}

func TestSleepRejectsInvalidDuration(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{}`,
		`{"duration_ms":0}`,
		`{"duration_ms":-1}`,
		`{"duration_ms":1.5}`,
		`{"duration_ms":"1"}`,
		`{"duration_ms":9223372036854775807}`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			payload, err := domain.ParsePayload([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Sleep(context.Background(), payload); err == nil {
				t.Fatalf("Sleep accepted payload %s", raw)
			}
		})
	}
}

func TestTestSideEffectHandlerAppendsOneDurableMarkerPerSuccess(t *testing.T) {
	t.Parallel()

	markerPath := filepath.Join(t.TempDir(), "side-effects.log")
	handler := NewTestSideEffectHandler(markerPath)
	payload, err := domain.ParsePayload([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := handler(context.Background(), payload)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(result.JSON()), `{"marker":"written"}`; got != want {
			t.Fatalf("result = %s, want %s", got, want)
		}
	}
	contents, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "completed\ncompleted\n"; got != want {
		t.Fatalf("marker contents = %q, want %q", got, want)
	}
}

func TestTestSideEffectHandlerHonorsPreCanceledContext(t *testing.T) {
	t.Parallel()

	markerPath := filepath.Join(t.TempDir(), "side-effects.log")
	payload, err := domain.ParsePayload([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewTestSideEffectHandler(markerPath)(ctx, payload); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want os.ErrNotExist", err)
	}
}

func TestRegistryContainsOnlyDemonstrationHandlers(t *testing.T) {
	t.Parallel()

	registry := Registry()
	if len(registry) != 3 || registry[EchoType] == nil || registry[PayloadSizeType] == nil || registry[SleepType] == nil {
		t.Fatalf("registry = %#v", registry)
	}
	if registry[TestSideEffectType] != nil {
		t.Fatal("default registry contains the test-only side-effect handler")
	}
}
