package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/worker"
)

const (
	EchoType             = "demo.echo"
	PayloadSizeType      = "demo.payload_size"
	SleepType            = "demo.sleep"
	TestSideEffectType   = "test.side_effect"
	testSideEffectMarker = "completed\n"
)

func Registry() map[string]worker.Handler {
	return map[string]worker.Handler{
		EchoType:        Echo,
		PayloadSizeType: PayloadSize,
		SleepType:       Sleep,
	}
}

func NewTestSideEffectHandler(markerPath string) worker.Handler {
	return func(ctx context.Context, _ domain.Payload) (domain.Result, error) {
		if err := ctx.Err(); err != nil {
			return domain.Result{}, err
		}
		file, err := os.OpenFile(markerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return domain.Result{}, fmt.Errorf("open side-effect marker: %w", err)
		}
		if _, err := file.WriteString(testSideEffectMarker); err != nil {
			_ = file.Close()
			return domain.Result{}, fmt.Errorf("write side-effect marker: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return domain.Result{}, fmt.Errorf("sync side-effect marker: %w", err)
		}
		if err := file.Close(); err != nil {
			return domain.Result{}, fmt.Errorf("close side-effect marker: %w", err)
		}
		return domain.ParseResult([]byte(`{"marker":"written"}`))
	}
}

func Echo(_ context.Context, payload domain.Payload) (domain.Result, error) {
	return domain.ParseResult(payload.JSON())
}

func PayloadSize(_ context.Context, payload domain.Payload) (domain.Result, error) {
	return domain.ParseResult([]byte(fmt.Sprintf(`{"bytes":%d}`, len(payload.JSON()))))
}

func Sleep(ctx context.Context, payload domain.Payload) (domain.Result, error) {
	var request struct {
		DurationMilliseconds int64 `json:"duration_ms"`
	}
	if err := json.Unmarshal(payload.JSON(), &request); err != nil {
		return domain.Result{}, fmt.Errorf("decode sleep payload: %w", err)
	}
	if request.DurationMilliseconds <= 0 || request.DurationMilliseconds > math.MaxInt64/int64(time.Millisecond) {
		return domain.Result{}, errors.New("duration_ms must be a positive whole-millisecond integer")
	}

	timer := time.NewTimer(time.Duration(request.DurationMilliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return domain.Result{}, ctx.Err()
	case <-timer.C:
	}

	return domain.ParseResult([]byte(fmt.Sprintf(`{"slept_ms":%d}`, request.DurationMilliseconds)))
}
