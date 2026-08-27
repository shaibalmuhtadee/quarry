package telemetry

import (
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

func TestEventMetricsExposeExactFamiliesAndLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	jobType, err := domain.ParseJobType("demo.echo")
	if err != nil {
		t.Fatal(err)
	}

	metrics.JobSubmitted()
	metrics.AttemptCompleted(jobType, domain.AttemptStatusRetryableFailed, "temporary")
	metrics.JobExecutionCompleted(jobType, domain.AttemptOutcomeKindRetryableFailure, 250*time.Millisecond)
	metrics.JobSchedulingDelay(jobType, 2*time.Second)
	metrics.LeaseExpired(domain.JobStatusRetryWait)
	metrics.RetryScheduled(RetryReasonRetryableFailure)
	metrics.StaleReport()
	metrics.DispatchClaim(2)
	metrics.WorkerPollError("unavailable")

	wantFamilies := map[string][]string{
		"quarry_jobs_submitted_total":           {},
		"quarry_job_attempts_total":             {"error_code", "job_type", "outcome"},
		"quarry_job_execution_duration_seconds": {"job_type", "outcome"},
		"quarry_job_scheduling_delay_seconds":   {"job_type"},
		"quarry_lease_expirations_total":        {"outcome"},
		"quarry_retries_scheduled_total":        {"reason"},
		"quarry_stale_reports_total":            {},
		"quarry_dispatch_claim_size":            {},
		"quarry_worker_poll_errors_total":       {"error_code"},
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		wantLabels, exists := wantFamilies[family.GetName()]
		if !exists {
			continue
		}
		if len(family.GetMetric()) == 0 {
			t.Fatalf("metric family %q contains no metrics", family.GetName())
		}
		gotLabels := make([]string, 0, len(family.GetMetric()[0].GetLabel()))
		for _, label := range family.GetMetric()[0].GetLabel() {
			gotLabels = append(gotLabels, label.GetName())
		}
		slices.Sort(gotLabels)
		if !slices.Equal(gotLabels, wantLabels) {
			t.Errorf("metric family %q labels = %v, want %v", family.GetName(), gotLabels, wantLabels)
		}
		delete(wantFamilies, family.GetName())
	}
	for name := range wantFamilies {
		t.Errorf("metric family %q was not gathered", name)
	}

	if got := testutil.ToFloat64(metrics.jobsSubmitted); got != 1 {
		t.Fatalf("jobs submitted = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.jobAttempts.WithLabelValues("demo.echo", "retryable_failed", "temporary")); got != 1 {
		t.Fatalf("attempts = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.leaseExpirations.WithLabelValues("retry_wait")); got != 1 {
		t.Fatalf("lease expirations = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.retriesScheduled.WithLabelValues("retryable_failure")); got != 1 {
		t.Fatalf("retries scheduled = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.staleReports); got != 1 {
		t.Fatalf("stale reports = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.workerPollErrors.WithLabelValues("unavailable")); got != 1 {
		t.Fatalf("poll errors = %v, want 1", got)
	}
}

func TestNewMetricsRejectsDuplicateRegistration(t *testing.T) {
	registry := prometheus.NewRegistry()
	if _, err := NewMetrics(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMetrics(registry); err == nil {
		t.Fatal("NewMetrics accepted duplicate metric registration")
	}
}
