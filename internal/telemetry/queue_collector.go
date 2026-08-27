package telemetry

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const queueSnapshotTimeout = 5 * time.Second

type QueueSnapshotSource interface {
	QueueSnapshot(context.Context) (QueueSnapshot, error)
}

type QueueSnapshot struct {
	Queued            int64
	RetryWait         int64
	OldestEligibleAge time.Duration
	ActiveJobs        int64
	ActiveWorkers     int64
}

type QueueHealthCollector struct {
	source            QueueSnapshotSource
	queueDepth        *prometheus.Desc
	oldestEligibleAge *prometheus.Desc
	activeJobs        *prometheus.Desc
	activeWorkers     *prometheus.Desc
	snapshotUp        *prometheus.Desc
}

func NewQueueHealthCollector(source QueueSnapshotSource) *QueueHealthCollector {
	return &QueueHealthCollector{
		source: source,
		queueDepth: prometheus.NewDesc(
			"quarry_queue_depth",
			"Current number of pending jobs by durable status.",
			[]string{"status"},
			nil,
		),
		oldestEligibleAge: prometheus.NewDesc(
			"quarry_oldest_queued_job_age_seconds",
			"Age of the oldest currently eligible queued or retry-wait job.",
			nil,
			nil,
		),
		activeJobs: prometheus.NewDesc(
			"quarry_active_jobs",
			"Current number of running jobs.",
			nil,
			nil,
		),
		activeWorkers: prometheus.NewDesc(
			"quarry_active_workers",
			"Current number of workers in the active durable state.",
			nil,
			nil,
		),
		snapshotUp: prometheus.NewDesc(
			"quarry_queue_snapshot_up",
			"Whether the latest queue snapshot query succeeded.",
			nil,
			nil,
		),
	}
}

func (collector *QueueHealthCollector) Describe(descriptions chan<- *prometheus.Desc) {
	descriptions <- collector.queueDepth
	descriptions <- collector.oldestEligibleAge
	descriptions <- collector.activeJobs
	descriptions <- collector.activeWorkers
	descriptions <- collector.snapshotUp
}

func (collector *QueueHealthCollector) Collect(metrics chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), queueSnapshotTimeout)
	defer cancel()

	snapshot, err := collector.source.QueueSnapshot(ctx)
	if err != nil {
		metrics <- prometheus.MustNewConstMetric(collector.snapshotUp, prometheus.GaugeValue, 0)
		return
	}

	metrics <- prometheus.MustNewConstMetric(collector.queueDepth, prometheus.GaugeValue, float64(snapshot.Queued), "queued")
	metrics <- prometheus.MustNewConstMetric(collector.queueDepth, prometheus.GaugeValue, float64(snapshot.RetryWait), "retry_wait")
	metrics <- prometheus.MustNewConstMetric(collector.oldestEligibleAge, prometheus.GaugeValue, snapshot.OldestEligibleAge.Seconds())
	metrics <- prometheus.MustNewConstMetric(collector.activeJobs, prometheus.GaugeValue, float64(snapshot.ActiveJobs))
	metrics <- prometheus.MustNewConstMetric(collector.activeWorkers, prometheus.GaugeValue, float64(snapshot.ActiveWorkers))
	metrics <- prometheus.MustNewConstMetric(collector.snapshotUp, prometheus.GaugeValue, 1)
}
