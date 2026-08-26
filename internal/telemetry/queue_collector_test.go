package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestQueueHealthCollectorExposesDatabaseSnapshot(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewQueueHealthCollector(queueSnapshotStub{snapshot: QueueSnapshot{
		Queued:            3,
		RetryWait:         2,
		OldestEligibleAge: 15 * time.Second,
		ActiveJobs:        4,
		ActiveWorkers:     5,
	}}))

	want := `
# HELP quarry_active_jobs Current number of running jobs.
# TYPE quarry_active_jobs gauge
quarry_active_jobs 4
# HELP quarry_active_workers Current number of workers in the active durable state.
# TYPE quarry_active_workers gauge
quarry_active_workers 5
# HELP quarry_oldest_queued_job_age_seconds Age of the oldest currently eligible queued or retry-wait job.
# TYPE quarry_oldest_queued_job_age_seconds gauge
quarry_oldest_queued_job_age_seconds 15
# HELP quarry_queue_depth Current number of pending jobs by durable status.
# TYPE quarry_queue_depth gauge
quarry_queue_depth{status="queued"} 3
quarry_queue_depth{status="retry_wait"} 2
# HELP quarry_queue_snapshot_up Whether the latest queue snapshot query succeeded.
# TYPE quarry_queue_snapshot_up gauge
quarry_queue_snapshot_up 1
`
	if err := testutil.GatherAndCompare(
		registry,
		strings.NewReader(want),
		"quarry_active_jobs",
		"quarry_active_workers",
		"quarry_oldest_queued_job_age_seconds",
		"quarry_queue_depth",
		"quarry_queue_snapshot_up",
	); err != nil {
		t.Fatal(err)
	}
}

func TestQueueHealthCollectorReportsFailureWithoutStaleGauges(t *testing.T) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(NewQueueHealthCollector(queueSnapshotStub{err: errors.New("database unavailable")}))

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "quarry_queue_snapshot_up" {
		names := make([]string, 0, len(families))
		for _, family := range families {
			names = append(names, family.GetName())
		}
		t.Fatalf("gathered families = %v, want only quarry_queue_snapshot_up", names)
	}
	if got := families[0].GetMetric()[0].GetGauge().GetValue(); got != 0 {
		t.Fatalf("queue snapshot up = %v, want 0", got)
	}
}

type queueSnapshotStub struct {
	snapshot QueueSnapshot
	err      error
}

func (stub queueSnapshotStub) QueueSnapshot(context.Context) (QueueSnapshot, error) {
	return stub.snapshot, stub.err
}
