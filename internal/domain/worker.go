package domain

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var ErrInvalidWorkerID = errors.New("invalid worker ID")

type WorkerID struct {
	value uuid.UUID
}

func NewWorkerID() WorkerID {
	return WorkerID{value: uuid.New()}
}

func ParseWorkerID(value string) (WorkerID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return WorkerID{}, fmt.Errorf("%w: %q", ErrInvalidWorkerID, value)
	}

	return WorkerID{value: id}, nil
}

func (id WorkerID) String() string {
	return id.value.String()
}

func (id WorkerID) UUID() uuid.UUID {
	return id.value
}

func (id WorkerID) IsZero() bool {
	return id.value == uuid.Nil
}

func (id WorkerID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, ErrInvalidWorkerID
	}

	return []byte(id.String()), nil
}
