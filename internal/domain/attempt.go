package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidAttemptNumber = errors.New("invalid attempt number")
	ErrInvalidAttemptStatus = errors.New("invalid attempt status")
)

type AttemptNumber struct {
	value int32
}

func NewAttemptNumber(value int32) (AttemptNumber, error) {
	if value <= 0 {
		return AttemptNumber{}, fmt.Errorf("%w: must be positive", ErrInvalidAttemptNumber)
	}

	return AttemptNumber{value: value}, nil
}

func (number AttemptNumber) Int32() int32 {
	return number.value
}

func (number AttemptNumber) IsZero() bool {
	return number.value == 0
}

type AttemptStatus string

const (
	AttemptStatusRunning   AttemptStatus = "running"
	AttemptStatusSucceeded AttemptStatus = "succeeded"
)

func ParseAttemptStatus(value string) (AttemptStatus, error) {
	status := AttemptStatus(value)
	switch status {
	case AttemptStatusRunning, AttemptStatusSucceeded:
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidAttemptStatus, value)
	}
}
