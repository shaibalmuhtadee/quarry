package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

var ErrInvalidReaperConfig = errors.New("reaper interval, batch size, and worker liveness timeout must be positive")

type recoveryStore interface {
	RecoverExpiredAttempts(context.Context, int32, time.Duration) (int64, error)
}

type ReaperConfig struct {
	Interval              time.Duration
	BatchSize             int32
	WorkerLivenessTimeout time.Duration
}

type Reaper struct {
	store  recoveryStore
	config ReaperConfig
	logger *slog.Logger
}

func NewReaper(store recoveryStore, config ReaperConfig, logger *slog.Logger) (*Reaper, error) {
	if config.Interval <= 0 || config.BatchSize <= 0 || config.WorkerLivenessTimeout <= 0 {
		return nil, ErrInvalidReaperConfig
	}
	return &Reaper{store: store, config: config, logger: logger}, nil
}

func (reaper *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(reaper.config.Interval)
	defer ticker.Stop()

	for {
		recovered, err := reaper.store.RecoverExpiredAttempts(
			ctx,
			reaper.config.BatchSize,
			reaper.config.WorkerLivenessTimeout,
		)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reaper.logger.Error("expired attempt recovery failed", slog.Any("error", err))
		} else if recovered > 0 {
			reaper.logger.Info("expired attempts recovered", slog.Int64("jobs", recovered))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
