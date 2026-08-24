package handlers

import (
	"context"
	"testing"

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

func TestRegistryContainsOnlyDemonstrationHandlers(t *testing.T) {
	t.Parallel()

	registry := Registry()
	if len(registry) != 2 || registry[EchoType] == nil || registry[PayloadSizeType] == nil {
		t.Fatalf("registry = %#v", registry)
	}
}
