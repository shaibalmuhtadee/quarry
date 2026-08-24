package domain_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestParseResult(t *testing.T) {
	tests := []struct {
		name      string
		value     json.RawMessage
		want      string
		wantError error
	}{
		{name: "object", value: json.RawMessage(`{"message":"sent"}`), want: `{"message":"sent"}`},
		{name: "null", value: json.RawMessage(`null`), want: `null`},
		{name: "missing", wantError: domain.ErrInvalidResult},
		{name: "malformed", value: json.RawMessage(`{"message":`), wantError: domain.ErrInvalidResult},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := domain.ParseResult(test.value)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ParseResult error = %v, want %v", err, test.wantError)
			}
			if test.wantError != nil {
				return
			}
			if got := string(result.JSON()); got != test.want {
				t.Fatalf("result JSON = %s, want %s", got, test.want)
			}

			copyOfJSON := result.JSON()
			copyOfJSON[0] = 'x'
			if got := string(result.JSON()); got != test.want {
				t.Fatalf("mutating returned JSON changed result to %s", got)
			}
		})
	}
}
