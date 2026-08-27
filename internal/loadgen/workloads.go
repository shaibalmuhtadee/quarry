package loadgen

import (
	"errors"
	"fmt"
	"time"
)

type Workload string

const (
	WorkloadQueueOverhead Workload = "a"
	WorkloadSimulatedIO   Workload = "b"

	SimulatedIODuration = 25 * time.Millisecond
)

func ParseWorkload(value string) (Workload, error) {
	workload := Workload(value)
	switch workload {
	case WorkloadQueueOverhead, WorkloadSimulatedIO:
		return workload, nil
	default:
		return "", fmt.Errorf("unsupported workload %q; use a or b", value)
	}
}

func NewWorkloadFactory(workload Workload, seed int64, maxAttempts int32, timeout time.Duration) (SubmissionFactory, error) {
	if _, err := ParseWorkload(string(workload)); err != nil {
		return nil, err
	}
	if maxAttempts <= 0 {
		return nil, errors.New("maximum attempts must be positive")
	}
	if timeout <= 0 || timeout%time.Millisecond != 0 {
		return nil, errors.New("job timeout must be a positive whole number of milliseconds")
	}

	return func(sequence uint64) Submission {
		if workload == WorkloadSimulatedIO {
			payload := fmt.Appendf(nil, `{"duration_ms":%d,"seed":%d,"sequence":%d}`,
				SimulatedIODuration/time.Millisecond, seed, sequence)
			return Submission{JobType: "demo.sleep", Payload: payload, MaxAttempts: maxAttempts, Timeout: timeout}
		}
		payload := fmt.Appendf(nil, `{"seed":%d,"sequence":%d}`, seed, sequence)
		return Submission{JobType: "demo.echo", Payload: payload, MaxAttempts: maxAttempts, Timeout: timeout}
	}, nil
}
