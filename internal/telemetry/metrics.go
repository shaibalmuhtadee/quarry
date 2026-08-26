package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
)

type RetryReason string

const (
	RetryReasonRetryableFailure RetryReason = "retryable_failure"
	RetryReasonTimedOut         RetryReason = "timed_out"
	RetryReasonPanicked         RetryReason = "panicked"
	RetryReasonLeaseExpired     RetryReason = "lease_expired"
)

type Metrics struct {
	jobsSubmitted      prometheus.Counter
	jobAttempts        *prometheus.CounterVec
	jobExecution       *prometheus.HistogramVec
	jobSchedulingDelay *prometheus.HistogramVec
	leaseExpirations   *prometheus.CounterVec
	retriesScheduled   *prometheus.CounterVec
	staleReports       prometheus.Counter
	dispatchClaimSize  prometheus.Histogram
	workerPollErrors   *prometheus.CounterVec
}

func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{
		jobsSubmitted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "quarry_jobs_submitted_total",
			Help: "Number of newly committed logical jobs.",
		}),
		jobAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "quarry_job_attempts_total",
			Help: "Number of newly committed terminal job attempts.",
		}, []string{"job_type", "outcome", "error_code"}),
		jobExecution: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "quarry_job_execution_duration_seconds",
			Help:    "Duration of one completed worker handler invocation.",
			Buckets: prometheus.DefBuckets,
		}, []string{"job_type", "outcome"}),
		jobSchedulingDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "quarry_job_scheduling_delay_seconds",
			Help:    "Delay from a job becoming available until its committed claim.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		}, []string{"job_type"}),
		leaseExpirations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "quarry_lease_expirations_total",
			Help: "Number of committed expired-attempt recoveries.",
		}, []string{"outcome"}),
		retriesScheduled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "quarry_retries_scheduled_total",
			Help: "Number of committed transitions to retry_wait.",
		}, []string{"reason"}),
		staleReports: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "quarry_stale_reports_total",
			Help: "Number of attempt reports fenced by current durable state.",
		}),
		dispatchClaimSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "quarry_dispatch_claim_size",
			Help:    "Number of jobs in each successful acquisition result.",
			Buckets: []float64{0, 1, 2, 4, 8, 16, 32, 64, 128},
		}),
		workerPollErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "quarry_worker_poll_errors_total",
			Help: "Number of failed worker acquisition calls.",
		}, []string{"error_code"}),
	}

	collectors := []prometheus.Collector{
		metrics.jobsSubmitted,
		metrics.jobAttempts,
		metrics.jobExecution,
		metrics.jobSchedulingDelay,
		metrics.leaseExpirations,
		metrics.retriesScheduled,
		metrics.staleReports,
		metrics.dispatchClaimSize,
		metrics.workerPollErrors,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (metrics *Metrics) JobSubmitted() {
	metrics.jobsSubmitted.Inc()
}

func (metrics *Metrics) AttemptCompleted(jobType domain.JobType, outcome domain.AttemptStatus, errorCode string) {
	metrics.jobAttempts.WithLabelValues(jobType.String(), string(outcome), errorCode).Inc()
}

func (metrics *Metrics) JobExecutionCompleted(
	jobType domain.JobType,
	outcome domain.AttemptOutcomeKind,
	duration time.Duration,
) {
	metrics.jobExecution.WithLabelValues(jobType.String(), string(outcome)).Observe(duration.Seconds())
}

func (metrics *Metrics) JobSchedulingDelay(jobType domain.JobType, delay time.Duration) {
	metrics.jobSchedulingDelay.WithLabelValues(jobType.String()).Observe(delay.Seconds())
}

func (metrics *Metrics) LeaseExpired(outcome domain.JobStatus) {
	metrics.leaseExpirations.WithLabelValues(string(outcome)).Inc()
}

func (metrics *Metrics) RetryScheduled(reason RetryReason) {
	metrics.retriesScheduled.WithLabelValues(string(reason)).Inc()
}

func (metrics *Metrics) StaleReport() {
	metrics.staleReports.Inc()
}

func (metrics *Metrics) DispatchClaim(size int) {
	metrics.dispatchClaimSize.Observe(float64(size))
}

func (metrics *Metrics) WorkerPollError(code string) {
	metrics.workerPollErrors.WithLabelValues(code).Inc()
}
