package domain

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidResult = errors.New("invalid result")

type Result struct {
	value json.RawMessage
}

func ParseResult(value json.RawMessage) (Result, error) {
	if len(value) == 0 {
		return Result{}, fmt.Errorf("%w: value is required", ErrInvalidResult)
	}
	if !json.Valid(value) {
		return Result{}, fmt.Errorf("%w: value must contain one JSON value", ErrInvalidResult)
	}

	return Result{value: append(json.RawMessage(nil), value...)}, nil
}

func (result Result) JSON() json.RawMessage {
	return append(json.RawMessage(nil), result.value...)
}
