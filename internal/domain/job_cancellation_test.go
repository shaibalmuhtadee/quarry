package domain_test

import (
	"errors"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestPlanJobCancellation(t *testing.T) {
	tests := []struct {
		name             string
		status           domain.JobStatus
		alreadyRequested bool
		want             domain.JobCancellationTransition
		wantError        error
	}{
		{name: "queued", status: domain.JobStatusQueued, want: domain.JobCancellationFinish},
		{name: "retry wait", status: domain.JobStatusRetryWait, want: domain.JobCancellationFinish},
		{name: "running", status: domain.JobStatusRunning, want: domain.JobCancellationRequest},
		{name: "running requested", status: domain.JobStatusRunning, alreadyRequested: true, want: domain.JobCancellationNoChange},
		{name: "cancelled", status: domain.JobStatusCancelled, alreadyRequested: true, want: domain.JobCancellationNoChange},
		{name: "succeeded", status: domain.JobStatusSucceeded, wantError: domain.ErrJobCancellationConflict},
		{name: "dead lettered", status: domain.JobStatusDeadLettered, wantError: domain.ErrJobCancellationConflict},
		{name: "invalid", status: domain.JobStatus("invalid"), wantError: domain.ErrInvalidJobStatus},
		{name: "invalid pending request", status: domain.JobStatusQueued, alreadyRequested: true, wantError: domain.ErrInvalidJobStatus},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := domain.PlanJobCancellation(test.status, test.alreadyRequested)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("PlanJobCancellation error = %v, want %v", err, test.wantError)
			}
			if got != test.want {
				t.Fatalf("PlanJobCancellation = %d, want %d", got, test.want)
			}
		})
	}
}
