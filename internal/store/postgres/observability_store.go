package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	postgresdb "github.com/shaibalmuhtadee/quarry/internal/store/postgres/generated"
)

type QueueSnapshot struct {
	Queued            int64
	RetryWait         int64
	OldestEligibleAge time.Duration
	ActiveJobs        int64
	ActiveWorkers     int64
}

type QueueSnapshotStore struct {
	queries *postgresdb.Queries
}

func NewQueueSnapshotStore(pool *pgxpool.Pool) *QueueSnapshotStore {
	return &QueueSnapshotStore{queries: postgresdb.New(pool)}
}

func (store *QueueSnapshotStore) QueueSnapshot(ctx context.Context) (QueueSnapshot, error) {
	row, err := store.queries.GetQueueSnapshot(ctx)
	if err != nil {
		return QueueSnapshot{}, fmt.Errorf("get queue snapshot: %w", err)
	}
	return QueueSnapshot{
		Queued:            row.QueuedJobs,
		RetryWait:         row.RetryWaitJobs,
		OldestEligibleAge: time.Duration(row.OldestEligibleAgeSeconds * float64(time.Second)),
		ActiveJobs:        row.ActiveJobs,
		ActiveWorkers:     row.ActiveWorkers,
	}, nil
}
