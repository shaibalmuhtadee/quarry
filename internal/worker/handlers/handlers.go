package handlers

import (
	"context"
	"fmt"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/worker"
)

const (
	EchoType        = "demo.echo"
	PayloadSizeType = "demo.payload_size"
)

func Registry() map[string]worker.Handler {
	return map[string]worker.Handler{
		EchoType:        Echo,
		PayloadSizeType: PayloadSize,
	}
}

func Echo(_ context.Context, payload domain.Payload) (domain.Result, error) {
	return domain.ParseResult(payload.JSON())
}

func PayloadSize(_ context.Context, payload domain.Payload) (domain.Result, error) {
	return domain.ParseResult([]byte(fmt.Sprintf(`{"bytes":%d}`, len(payload.JSON()))))
}
