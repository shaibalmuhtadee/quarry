package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/shaibalmuhtadee/quarry/internal/telemetry"
)

var ErrInvalidReaperConfig = errors.New("reaper interval, batch size, and worker liveness timeout must be positive")

type recoveryStore interface {
	RecoverExpiredAttemptTransitions(context.Context, int32, time.Duration) ([]postgres.RecoveryTransition, error)
}

type recoveryMetrics interface {
	AttemptCompleted(domain.JobType, domain.AttemptStatus, string)
	LeaseExpired(domain.JobStatus)
	RetryScheduled(telemetry.RetryReason)
}

type ReaperConfig struct {
	Interval              time.Duration
	BatchSize             int32
	WorkerLivenessTimeout time.Duration
}

type Reaper struct {
	store   recoveryStore
	config  ReaperConfig
	logger  *slog.Logger
	metrics recoveryMetrics
}

func NewReaper(store recoveryStore, config ReaperConfig, logger *slog.Logger) (*Reaper, error) {
	return newReaper(store, config, logger, nil)
}

func NewReaperWithMetrics(
	store recoveryStore,
	config ReaperConfig,
	logger *slog.Logger,
	metrics recoveryMetrics,
) (*Reaper, error) {
	return newReaper(store, config, logger, metrics)
}

func newReaper(store recoveryStore, config ReaperConfig, logger *slog.Logger, metrics recoveryMetrics) (*Reaper, error) {
	if config.Interval <= 0 || config.BatchSize <= 0 || config.WorkerLivenessTimeout <= 0 {
		return nil, ErrInvalidReaperConfig
	}
	return &Reaper{store: store, config: config, logger: logger, metrics: metrics}, nil
}

func (reaper *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(reaper.config.Interval)
	defer ticker.Stop()

	for {
		transitions, err := reaper.store.RecoverExpiredAttemptTransitions(
			ctx,
			reaper.config.BatchSize,
			reaper.config.WorkerLivenessTimeout,
		)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reaper.logger.Error("expired attempt recovery failed", slog.Any("error", err))
		} else if len(transitions) > 0 {
			for _, transition := range transitions {
				if reaper.metrics != nil {
					reaper.metrics.AttemptCompleted(transition.JobType, transition.AttemptStatus, transition.ErrorCode)
					reaper.metrics.LeaseExpired(transition.JobStatus)
					if transition.JobStatus == domain.JobStatusRetryWait {
						reaper.metrics.RetryScheduled(telemetry.RetryReasonLeaseExpired)
					}
				}
				logCtx := telemetry.ContextFromTraceParent(ctx, transition.TraceParent)
				reaper.logger.InfoContext(
					logCtx,
					"attempt lease recovered",
					slog.String("job_id", transition.JobID.String()),
					slog.String("job_type", transition.JobType.String()),
					slog.Int("attempt_no", int(transition.AttemptNumber.Int32())),
					slog.String("job_outcome", string(transition.AttemptStatus)),
					slog.String("error_code", transition.ErrorCode),
				)
				if transition.JobStatus == domain.JobStatusRetryWait {
					reaper.logger.InfoContext(
						logCtx,
						"retry scheduled",
						slog.String("job_id", transition.JobID.String()),
						slog.String("job_type", transition.JobType.String()),
						slog.Int("attempt_no", int(transition.AttemptNumber.Int32())),
						slog.String("job_outcome", string(transition.AttemptStatus)),
						slog.String("error_code", transition.ErrorCode),
					)
				}
			}
			reaper.logger.Info("expired attempts recovered", slog.Int("jobs", len(transitions)))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
